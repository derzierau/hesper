package routines

import (
	"context"
	"fmt"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Options configure a Scheduler. Zero values take the defaults.
type Options struct {
	Now func() time.Time // the clock; time.Now
	// Tick bounds the wait between steps (60 s): the safety net for a
	// clock jump or a Mac waking from sleep.
	Tick time.Duration
	Logf func(string, ...any)
}

// Scheduler runs the routines' clock: a Step for each instant decides,
// for every routine due, whether to start a run (through the Runner),
// skip it or catch up, and records the outcome in the Store.
type Scheduler struct {
	store  *Store
	runner Runner
	opt    Options
}

// NewScheduler makes a scheduler over store that starts runs with runner.
func NewScheduler(store *Store, runner Runner, opt Options) *Scheduler {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Tick <= 0 {
		opt.Tick = time.Minute
	}
	if opt.Logf == nil {
		opt.Logf = func(string, ...any) {}
	}
	return &Scheduler{store: store, runner: runner, opt: opt}
}

// Step is the whole decision for one instant: every routine due at now is
// skipped (CatchUp "none" and the clock is late; Overlap "skip" and the
// previous run is live), has its live run stopped (Overlap "replace"), and
// is advanced to its next run before a run starts, so a slow start never
// fires twice. ctx bounds the starts and the wait for their ends: a
// daemon passes its lifetime.
func (s *Scheduler) Step(ctx context.Context, now time.Time) {
	for _, r := range s.store.Due(now) {
		if r.Policy.CatchUp == wire.CatchUpNone && Late(r, r.NextRun, now) {
			s.skip(r.ID, fmt.Sprintf("late by %s", now.Sub(r.NextRun).Round(time.Second)))
			s.advance(r.ID, now)
			continue
		}
		if s.runner.Live(r.ID) {
			if r.Policy.Overlap != wire.OverlapReplace {
				seq := 0
				if r.LastRun != nil {
					seq = r.LastRun.Seq
				}
				s.skip(r.ID, fmt.Sprintf("run %d is still live", seq))
				s.advance(r.ID, now)
				continue
			}
			if err := s.runner.Stop(ctx, r.ID); err != nil {
				s.opt.Logf("hesperd: routine %s: stopping the live run: %v", r.ID, err)
			}
		}
		if !s.advance(r.ID, now) {
			continue
		}
		if _, err := s.start(ctx, r.ID, false); err != nil {
			s.opt.Logf("hesperd: routine %s: %v", r.ID, err)
		}
	}
}

// RunNow starts a run by hand (routines.run): paused or not, without
// moving the clock. A live run is "exists".
func (s *Scheduler) RunNow(ctx context.Context, id string) (wire.RoutineRun, error) {
	r, err := s.store.Get(id)
	if err != nil {
		return wire.RoutineRun{}, err
	}
	if s.runner.Live(id) {
		seq := 0
		if r.LastRun != nil {
			seq = r.LastRun.Seq
		}
		return wire.RoutineRun{}, wire.Errorf(wire.CodeExists, "routine %s: run %d is still live", id, seq)
	}
	return s.start(ctx, id, true)
}

// start begins a run and starts it through the Runner; a start that fails
// is recorded as a failed run. Otherwise the run names its agent at once
// and its end is recorded when the Runner reports it (or the wait ends
// with ctx).
func (s *Scheduler) start(ctx context.Context, id string, manual bool) (wire.RoutineRun, error) {
	r, run, err := s.store.BeginRun(id, manual)
	if err != nil {
		return wire.RoutineRun{}, err
	}
	agent, ch, err := s.runner.Start(ctx, r, run)
	if err != nil {
		run.Outcome, run.Error, run.Ended = wire.RunFailed, err.Error(), s.opt.Now().UTC()
		if e := s.store.EndRun(id, run); e != nil {
			s.opt.Logf("hesperd: routine %s: recording the failed run: %v", id, e)
		}
		return wire.RoutineRun{}, fmt.Errorf("starting run %d: %w", run.Seq, err)
	}
	run.Agent = agent
	if err := s.store.UpdateRun(id, run); err != nil {
		s.opt.Logf("hesperd: routine %s: recording run %d's agent: %v", id, run.Seq, err)
	}
	go func() {
		select {
		case ended, ok := <-ch:
			if !ok {
				return
			}
			// The record is the scheduler's: the run it began, ended as
			// the Runner says.
			ended.Seq, ended.Started, ended.Manual = run.Seq, run.Started, run.Manual
			if ended.Outcome == "" || ended.Outcome == wire.RunRunning {
				ended.Outcome = wire.RunExited
			}
			if err := s.store.EndRun(id, ended); err != nil {
				s.opt.Logf("hesperd: routine %s: recording run %d: %v", id, ended.Seq, err)
			}
		case <-ctx.Done():
		}
	}()
	return run, nil
}

func (s *Scheduler) skip(id, reason string) {
	if _, err := s.store.RecordSkipped(id, reason, false); err != nil {
		s.opt.Logf("hesperd: routine %s: recording the skipped run: %v", id, err)
	}
}

func (s *Scheduler) advance(id string, now time.Time) bool {
	if _, err := s.store.Advance(id, now); err != nil {
		s.opt.Logf("hesperd: routine %s: %v", id, err)
		return false
	}
	return true
}

// Run steps until ctx ends: after each step it waits for the next
// NextRun (at most Tick) or a change to a routine's clock.
func (s *Scheduler) Run(ctx context.Context) {
	// Go 1.23 timers: Reset on a timer that fired or stopped needs no
	// drain of its channel.
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	for {
		now := s.opt.Now()
		s.Step(ctx, now)
		wait := s.opt.Tick
		if next := s.nextDue(); !next.IsZero() {
			switch d := next.Sub(now); {
			case d <= 0:
				// Still due after the step (it could not be advanced):
				// not a hot loop.
				wait = min(wait, time.Second)
			case d < wait:
				wait = d
			}
		}
		timer.Reset(wait)
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-s.store.Changed():
			timer.Stop()
		}
	}
}

// nextDue is the earliest NextRun of the unpaused routines (zero: none).
func (s *Scheduler) nextDue() time.Time {
	var next time.Time
	for _, r := range s.store.List() {
		if !r.Paused && !r.NextRun.IsZero() && (next.IsZero() || r.NextRun.Before(next)) {
			next = r.NextRun
		}
	}
	return next
}
