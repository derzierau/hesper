package host

import (
	"testing"
	"time"
)

func TestDropTrackerCountsTheLastFiveMinutes(t *testing.T) {
	var d dropTracker
	t0 := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	if s := d.session(t0); s != nil {
		t.Fatalf("no drops yet: %+v", s)
	}
	for i := range 7 {
		d.record(t0.Add(time.Duration(i)*40*time.Second), "reason")
	}
	last := t0.Add(7 * 40 * time.Second)
	d.record(last, "relay closed the connection: message too big")
	if s := d.session(last); s.Drops5m != 8 || s.LastReason != "relay closed the connection: message too big" || !s.LastAt.Equal(last) {
		t.Fatalf("after eight drops: %+v", s)
	}
	// Five minutes after t0 the first drop has left the window.
	if s := d.session(t0.Add(5*time.Minute + time.Second)); s.Drops5m != 7 {
		t.Fatalf("window: %d", s.Drops5m)
	}
	// Long after, nothing counts but the last drop is still told.
	later := last.Add(time.Hour)
	if s := d.session(later); s.Drops5m != 0 || !s.LastAt.Equal(last) {
		t.Fatalf("an hour later: %+v", s)
	}
	// Old drops are let go of, so a host flapping for days stays small.
	d.record(later, "again")
	if len(d.times) != 1 {
		t.Fatalf("kept %d drop times", len(d.times))
	}
}
