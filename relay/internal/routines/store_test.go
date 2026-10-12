package routines

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// clock is a fake clock the tests move by hand.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newClock(t *testing.T, s string) *clock { return &clock{at(t, time.UTC, s)} }

func openStore(t *testing.T, dir string, c *clock) *Store {
	t.Helper()
	s, err := Open(StoreOptions{Dir: dir, Loc: time.UTC, Logf: t.Logf, Now: c.now})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func code(err error) string {
	var we *wire.Error
	if errors.As(err, &we) {
		return we.Code
	}
	return fmt.Sprint(err)
}

func TestStoreSaveListRestart(t *testing.T) {
	dir := t.TempDir()
	c := newClock(t, "2026-03-10 09:04")
	s := openStore(t, dir, c)
	r, err := s.Save(wire.Routine{Every: "1h", Offset: "5m", CreatedBy: "local", Spawn: wire.SpawnParams{Task: "tidy", Project: "/srv/app"}})
	if err != nil {
		t.Fatal(err)
	}
	if !ID.MatchString(r.ID) || r.Name != strings.TrimPrefix(r.ID, "r-") || !r.Created.Equal(c.t) || !r.Updated.Equal(c.t) || r.CreatedBy != "local" {
		t.Fatalf("saved %+v", r)
	}
	if !r.NextRun.Equal(at(t, time.UTC, "2026-03-10 09:05")) || r.Policy.Timeout != "1h" {
		t.Fatalf("next run %v", r.NextRun)
	}
	// Save again: Created and CreatedBy stay, Updated moves, Runs are kept.
	c.t = c.t.Add(time.Minute)
	if _, _, err := s.BeginRun(r.ID, false); err != nil {
		t.Fatal(err)
	}
	again := r
	again.Name, again.CreatedBy, again.Runs, again.Created = "Tidy", "someone", 99, time.Time{}
	again, err = s.Save(again)
	if err != nil {
		t.Fatal(err)
	}
	if again.Name != "Tidy" || !again.Created.Equal(r.Created) || again.CreatedBy != "local" || !again.Updated.Equal(c.t) || again.Runs != 1 || again.LastRun == nil || again.LastRun.Seq != 1 {
		t.Fatalf("resave %+v", again)
	}
	// A second routine, created later, lists after the first.
	c.t = c.t.Add(time.Minute)
	second, err := s.Save(wire.Routine{ID: "r-second", At: []string{"18:00"}, Spawn: wire.SpawnParams{Task: "report", Project: "/srv/app"}})
	if err != nil {
		t.Fatal(err)
	}
	if list := s.List(); len(list) != 2 || list[0].ID != r.ID || list[1].ID != "r-second" {
		t.Fatalf("list %+v", list)
	}
	if _, err := s.Get("r-nope"); code(err) != wire.CodeNotFound {
		t.Fatalf("get: %v", err)
	}
	if _, err := s.Save(wire.Routine{ID: "r-bad", Spawn: wire.SpawnParams{Task: "x", Project: "/p"}}); code(err) != wire.CodeInvalid {
		t.Fatalf("invalid: %v", err)
	}
	// The file is 0600, in the state directory, and the runs directory exists.
	st, err := os.Stat(filepath.Join(dir, "routines.json"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("routines.json %v %v", st, err)
	}
	if st, err := os.Stat(filepath.Join(dir, "routines")); err != nil || !st.IsDir() {
		t.Fatalf("routines/ %v %v", st, err)
	}

	// Restart with the clock a day on: the first routine's NextRun is past
	// and kept (due: its policy catches up), its run count and running
	// LastRun survive; the second's future NextRun is recomputed.
	c.t = at(t, time.UTC, "2026-03-11 12:00")
	s2 := openStore(t, dir, c)
	got, err := s2.Get(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Runs != 1 || got.LastRun == nil || got.LastRun.Outcome != wire.RunRunning || !got.NextRun.Equal(again.NextRun) || got.Name != "Tidy" {
		t.Fatalf("after restart %+v", got)
	}
	if due := s2.Due(c.t); len(due) != 2 || due[0].ID != r.ID {
		t.Fatalf("due %+v", due)
	}
	// Reopened with the clock before the saved NextRun: recomputed from now.
	c.t = at(t, time.UTC, "2026-03-09 12:00")
	s3 := openStore(t, dir, c)
	got, _ = s3.Get(second.ID)
	if !got.NextRun.Equal(at(t, time.UTC, "2026-03-09 18:00")) {
		t.Fatalf("recomputed %v", got.NextRun)
	}
}

func TestStoreBrokenFileAndLimits(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "routines.json"), []byte("{nope"), 0o600)
	c := newClock(t, "2026-03-10 09:00")
	s := openStore(t, dir, c)
	if len(s.List()) != 0 {
		t.Fatal("want empty")
	}
	if _, err := os.Stat(filepath.Join(dir, "routines.json.broken")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < RoutineLimit; i++ {
		if _, err := s.Save(wire.Routine{ID: fmt.Sprintf("r-%d", i), Every: "1h", Spawn: wire.SpawnParams{Task: "x", Project: "/p"}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Save(wire.Routine{Every: "1h", Spawn: wire.SpawnParams{Task: "x", Project: "/p"}}); code(err) != wire.CodeInvalid {
		t.Fatalf("over the limit: %v", err)
	}
	// Replacing an existing one is fine at the limit.
	if _, err := s.Save(wire.Routine{ID: "r-0", Every: "2h", Spawn: wire.SpawnParams{Task: "x", Project: "/p"}}); err != nil {
		t.Fatal(err)
	}
	// A file with a bad id and a bad cadence loses those routines only.
	dir2 := t.TempDir()
	os.WriteFile(filepath.Join(dir2, "routines.json"), []byte(`{"version":1,"routines":[
		{"id":"d-x","every":"1h","spawn":{"task":"x","project":"/p"}},
		{"id":"r-nocadence","spawn":{"task":"x","project":"/p"}},
		{"id":"r-ok","every":"1h","spawn":{"task":"x","project":"/p"}}]}`), 0o600)
	s2 := openStore(t, dir2, c)
	if list := s2.List(); len(list) != 1 || list[0].ID != "r-ok" || list[0].Policy.KeepRuns != DefaultKeepRuns || list[0].NextRun.IsZero() {
		t.Fatalf("list %+v", list)
	}
}

func TestStorePauseAdvanceDue(t *testing.T) {
	c := newClock(t, "2026-03-10 09:00")
	s := openStore(t, "", c)
	a, _ := s.Save(wire.Routine{ID: "r-a", Every: "1h", Spawn: wire.SpawnParams{Task: "x", Project: "/p"}})
	b, _ := s.Save(wire.Routine{ID: "r-b", Every: "30m", Spawn: wire.SpawnParams{Task: "x", Project: "/p"}})
	if !a.NextRun.Equal(at(t, time.UTC, "2026-03-10 10:00")) || !b.NextRun.Equal(at(t, time.UTC, "2026-03-10 09:30")) {
		t.Fatalf("next %v %v", a.NextRun, b.NextRun)
	}
	if due := s.Due(c.t); len(due) != 0 {
		t.Fatalf("due now %+v", due)
	}
	// Both due: the earliest NextRun first.
	if due := s.Due(at(t, time.UTC, "2026-03-10 10:00")); len(due) != 2 || due[0].ID != "r-b" || due[1].ID != "r-a" {
		t.Fatalf("due %+v", due)
	}
	// Paused: NextRun zero, never due, Advance leaves it; resumed from now.
	c.t = at(t, time.UTC, "2026-03-10 09:10")
	p, err := s.SetPaused("r-a", true)
	if err != nil || !p.Paused || !p.NextRun.IsZero() || !p.Updated.Equal(c.t) {
		t.Fatalf("paused %+v %v", p, err)
	}
	if due := s.Due(at(t, time.UTC, "2026-03-11 00:00")); len(due) != 1 || due[0].ID != "r-b" {
		t.Fatalf("due while paused %+v", due)
	}
	if adv, _ := s.Advance("r-a", c.t); !adv.NextRun.IsZero() {
		t.Fatalf("advanced while paused %v", adv.NextRun)
	}
	// A paused routine saved again stays paused with a zero NextRun.
	p.Name = "renamed"
	if p, err = s.Save(p); err != nil || !p.Paused || !p.NextRun.IsZero() {
		t.Fatalf("saved paused %+v %v", p, err)
	}
	if p, _ = s.SetPaused("r-a", false); p.Paused || !p.NextRun.Equal(at(t, time.UTC, "2026-03-10 10:00")) {
		t.Fatalf("resumed %+v", p)
	}
	if adv, _ := s.Advance("r-b", at(t, time.UTC, "2026-03-10 09:30")); !adv.NextRun.Equal(at(t, time.UTC, "2026-03-10 10:00")) {
		t.Fatalf("advanced %v", adv.NextRun)
	}
	if _, err := s.SetPaused("r-nope", true); code(err) != wire.CodeNotFound {
		t.Fatal(err)
	}
	if _, err := s.Advance("r-nope", c.t); code(err) != wire.CodeNotFound {
		t.Fatal(err)
	}
}

func TestStoreRuns(t *testing.T) {
	for _, dir := range []string{"", t.TempDir()} {
		t.Run(map[bool]string{true: "memory", false: "files"}[dir == ""], func(t *testing.T) {
			c := newClock(t, "2026-03-10 09:00")
			s := openStore(t, dir, c)
			if _, err := s.Save(wire.Routine{ID: "r-a", Every: "1h", Policy: wire.RunPolicy{KeepRuns: 3}, Spawn: wire.SpawnParams{Task: "x", Project: "/p"}}); err != nil {
				t.Fatal(err)
			}
			if runs, err := s.Runs("r-a", 0); err != nil || len(runs) != 0 {
				t.Fatalf("no runs yet: %+v %v", runs, err)
			}
			r, run, err := s.BeginRun("r-a", false)
			if err != nil || run.Seq != 1 || run.Outcome != wire.RunRunning || !run.Started.Equal(c.t) || run.Manual || r.Runs != 1 || r.LastRun.Seq != 1 {
				t.Fatalf("begin %+v %+v %v", r, run, err)
			}
			// A running run not in the file yet comes first.
			if runs, _ := s.Runs("r-a", 0); len(runs) != 1 || runs[0].Seq != 1 || runs[0].Outcome != wire.RunRunning {
				t.Fatalf("running %+v", runs)
			}
			c.t = c.t.Add(10 * time.Minute)
			run.Agent, run.Session, run.Outcome, run.State, run.Message = "L/a1", "s-1", wire.RunDone, wire.StateDone, "all tidy"
			if err := s.EndRun("r-a", run); err != nil {
				t.Fatal(err)
			}
			r, _ = s.Get("r-a")
			if r.LastRun.Outcome != wire.RunDone || !r.LastRun.Ended.Equal(c.t) || r.LastRun.Agent != "L/a1" || r.LastRun.Message != "all tidy" {
				t.Fatalf("ended %+v", r.LastRun)
			}
			if runs, _ := s.Runs("r-a", 0); len(runs) != 1 || runs[0].Seq != 1 || runs[0].Session != "s-1" || runs[0].Outcome != wire.RunDone {
				t.Fatalf("one run %+v", runs)
			}
			// An older run ending after a newer one began leaves LastRun alone.
			_, run2, _ := s.BeginRun("r-a", true)
			_, run3, _ := s.BeginRun("r-a", false)
			run2.Outcome = wire.RunExited
			s.EndRun("r-a", run2)
			r, _ = s.Get("r-a")
			if r.LastRun.Seq != 3 || r.LastRun.Outcome != wire.RunRunning {
				t.Fatalf("last run %+v", r.LastRun)
			}
			// Newest first: the running 3, then 2, 1.
			if runs, _ := s.Runs("r-a", 0); len(runs) != 3 || runs[0].Seq != 3 || runs[1].Seq != 2 || !runs[1].Manual || runs[2].Seq != 1 {
				t.Fatalf("order %+v", runs)
			}
			run3.Outcome = wire.RunError
			s.EndRun("r-a", run3)
			// Skipped: begun and ended at once with the reason.
			skipped, err := s.RecordSkipped("r-a", "run 3 is still live", false)
			if err != nil || skipped.Seq != 4 || skipped.Outcome != wire.RunSkipped || skipped.Error != "run 3 is still live" || !skipped.Ended.Equal(skipped.Started) {
				t.Fatalf("skipped %+v %v", skipped, err)
			}
			// Trimmed to KeepRuns (3): run 1 is gone; the limit applies.
			runs, _ := s.Runs("r-a", 0)
			if len(runs) != 3 || runs[0].Seq != 4 || runs[2].Seq != 2 {
				t.Fatalf("trimmed %+v", runs)
			}
			if runs, _ := s.Runs("r-a", 2); len(runs) != 2 || runs[1].Seq != 3 {
				t.Fatalf("limit %+v", runs)
			}
			if dir != "" {
				path := filepath.Join(dir, "routines", "r-a.runs.jsonl")
				st, err := os.Stat(path)
				if err != nil || st.Mode().Perm() != 0o600 {
					t.Fatalf("runs file %v %v", st, err)
				}
				data, _ := os.ReadFile(path)
				if n := strings.Count(string(data), "\n"); n != 3 {
					t.Fatalf("%d lines:\n%s", n, data)
				}
				// Removing the routine removes the file.
				if err := s.Remove("r-a"); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("runs file after remove: %v", err)
				}
			} else if err := s.Remove("r-a"); err != nil {
				t.Fatal(err)
			}
			if err := s.Remove("r-a"); code(err) != wire.CodeNotFound {
				t.Fatal(err)
			}
			if _, err := s.Runs("r-a", 0); code(err) != wire.CodeNotFound {
				t.Fatal(err)
			}
			if err := s.EndRun("r-a", run); code(err) != wire.CodeNotFound {
				t.Fatal(err)
			}
			if _, _, err := s.BeginRun("r-a", false); code(err) != wire.CodeNotFound {
				t.Fatal(err)
			}
		})
	}
}

func TestStoreSubscribeAndChanged(t *testing.T) {
	c := newClock(t, "2026-03-10 09:00")
	s := openStore(t, "", c)
	s.Save(wire.Routine{ID: "r-a", Every: "1h", Spawn: wire.SpawnParams{Task: "x", Project: "/p"}})
	drain := func(ch <-chan struct{}) bool {
		select {
		case <-ch:
			return true
		default:
			return false
		}
	}
	if !drain(s.Changed()) {
		t.Fatal("save did not signal Changed")
	}
	sub := s.Subscribe()
	defer s.Unsubscribe(sub)
	<-sub.Wake()
	if changed, removed := sub.Take(); len(changed) != 1 || changed[0].ID != "r-a" || len(removed) != 0 {
		t.Fatalf("initial %+v %v", changed, removed)
	}
	// A run beginning changes the routine but not its clock.
	s.BeginRun("r-a", false)
	<-sub.Wake()
	if changed, _ := sub.Take(); len(changed) != 1 || changed[0].LastRun == nil {
		t.Fatalf("begin %+v", changed)
	}
	if drain(s.Changed()) {
		t.Fatal("BeginRun signalled Changed")
	}
	// Two saves coalesce to the latest; pause signals Changed.
	s.Save(wire.Routine{ID: "r-b", Every: "1h", Spawn: wire.SpawnParams{Task: "x", Project: "/p"}})
	s.SetPaused("r-b", true)
	<-sub.Wake()
	if changed, _ := sub.Take(); len(changed) != 1 || changed[0].ID != "r-b" || !changed[0].Paused {
		t.Fatalf("coalesced %+v", changed)
	}
	if !drain(s.Changed()) {
		t.Fatal("pause did not signal Changed")
	}
	s.Remove("r-b")
	<-sub.Wake()
	if changed, removed := sub.Take(); len(changed) != 0 || len(removed) != 1 || removed[0] != "r-b" {
		t.Fatalf("removed %+v %v", changed, removed)
	}
	if !drain(s.Changed()) {
		t.Fatal("remove did not signal Changed")
	}
	// Unsubscribed: nothing more arrives.
	s.Unsubscribe(sub)
	s.Save(wire.Routine{ID: "r-c", Every: "1h", Spawn: wire.SpawnParams{Task: "x", Project: "/p"}})
	if changed, _ := sub.Take(); len(changed) != 0 {
		t.Fatalf("after unsubscribe %+v", changed)
	}
}
