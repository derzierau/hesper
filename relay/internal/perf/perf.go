// Package perf records how long the host spends in each stage of a request
// or a keystroke (decrypt, authorize, PTY input, output, encrypt), for the
// latency harness (internal/transport/latency_test.go) and for debugging
// lag. Recording is off until Enable; a disabled Observe is one atomic load.
package perf

import (
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// Stage names. A keystroke on a remote attach passes stream.open (decrypt
// the link's frame) and stream.input (hand the bytes to the agent's attach,
// then internal/ptyhost's hesperd.input to the PTY); the echo passes
// hesperd.output (PTY read to the viewers) and stream.seal (encrypt the
// frame). A request passes rpc.open, rpc.authorize, rpc.execute and
// rpc.seal.
const (
	StreamOpen   = "stream.open"
	StreamInput  = "stream.input"
	StreamRender = "stream.render"
	StreamSeal   = "stream.seal"
	RPCOpen      = "rpc.open"
	RPCAuthorize = "rpc.authorize"
	RPCExecute   = "rpc.execute"
	RPCSeal      = "rpc.seal"
)

const keep = 4096 // samples per stage (a ring)

type ring struct {
	samples []time.Duration
	next    int
	count   int
}

// Recorder keeps the most recent samples of each stage.
type Recorder struct {
	on     atomic.Bool
	mu     sync.Mutex
	stages map[string]*ring
}

// Host is the host process's recorder.
var Host = &Recorder{}

// Enable starts (or stops) recording.
func (r *Recorder) Enable(on bool) { r.on.Store(on) }

// Enabled reports whether samples are recorded.
func (r *Recorder) Enabled() bool { return r.on.Load() }

// Observe records one sample of a stage.
func (r *Recorder) Observe(stage string, d time.Duration) {
	if !r.on.Load() {
		return
	}
	r.mu.Lock()
	if r.stages == nil {
		r.stages = map[string]*ring{}
	}
	s := r.stages[stage]
	if s == nil {
		s = &ring{samples: make([]time.Duration, keep)}
		r.stages[stage] = s
	}
	s.samples[s.next] = d
	s.next = (s.next + 1) % keep
	s.count++
	r.mu.Unlock()
}

// Since records the time since start.
func (r *Recorder) Since(stage string, start time.Time) {
	if r.on.Load() {
		r.Observe(stage, time.Since(start))
	}
}

// Reset forgets every sample.
func (r *Recorder) Reset() {
	r.mu.Lock()
	r.stages = nil
	r.mu.Unlock()
}

// Stats summarizes samples.
type Stats struct {
	N                  int
	P50, P95, P99, Max time.Duration
}

// Summarize computes percentiles (nearest rank) of samples.
func Summarize(samples []time.Duration) Stats {
	if len(samples) == 0 {
		return Stats{}
	}
	s := slices.Clone(samples)
	slices.Sort(s)
	at := func(p float64) time.Duration {
		i := int(p*float64(len(s))+0.999999) - 1
		return s[min(max(i, 0), len(s)-1)]
	}
	return Stats{N: len(s), P50: at(0.50), P95: at(0.95), P99: at(0.99), Max: s[len(s)-1]}
}

// Stages lists the stages with samples, sorted.
func (r *Recorder) Stages() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var names []string
	for name := range r.stages {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// Summary returns the stats of one stage's retained samples.
func (r *Recorder) Summary(stage string) Stats {
	r.mu.Lock()
	s := r.stages[stage]
	var samples []time.Duration
	if s != nil {
		samples = slices.Clone(s.samples[:min(s.count, keep)])
	}
	r.mu.Unlock()
	return Summarize(samples)
}
