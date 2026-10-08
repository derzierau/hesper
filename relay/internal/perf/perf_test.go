package perf

import (
	"testing"
	"time"
)

func TestSummarize(t *testing.T) {
	var samples []time.Duration
	for i := 1; i <= 100; i++ {
		samples = append(samples, time.Duration(i)*time.Millisecond)
	}
	s := Summarize(samples)
	if s.N != 100 || s.P50 != 50*time.Millisecond || s.P95 != 95*time.Millisecond || s.P99 != 99*time.Millisecond || s.Max != 100*time.Millisecond {
		t.Fatalf("%+v", s)
	}
	if (Summarize(nil) != Stats{}) {
		t.Fatal("empty")
	}
}

func TestRecorderOffByDefaultAndRing(t *testing.T) {
	r := &Recorder{}
	r.Observe("x", time.Second)
	if r.Summary("x").N != 0 || len(r.Stages()) != 0 {
		t.Fatal("recorded while off")
	}
	r.Enable(true)
	for i := range keep + 10 {
		r.Observe("x", time.Duration(i))
	}
	if s := r.Summary("x"); s.N != keep || s.Max != time.Duration(keep+9) {
		t.Fatalf("%+v", s)
	}
	r.Reset()
	if r.Summary("x").N != 0 {
		t.Fatal("reset")
	}
}
