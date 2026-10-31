package crdt

import (
	"testing"
	"time"
)

func TestClockAndField(t *testing.T) {
	now := time.UnixMilli(1000)
	c := Clock{Node: "a", Now: func() time.Time { return now }}
	s1, s2 := c.Tick(), c.Tick()
	if !s1.Less(s2) || s2 != (Stamp{T: 1000, C: 1, N: "a"}) {
		t.Fatalf("ticks %+v %+v", s1, s2)
	}
	c.Observe(Stamp{T: 5000, C: 3, N: "b"})
	if s := c.Tick(); s != (Stamp{T: 5000, C: 4, N: "a"}) {
		t.Fatalf("after observe %+v", s)
	}
	c.Observe(Stamp{T: now.Add(25 * time.Hour).UnixMilli()})
	if c.Last.T != 5000 {
		t.Fatal("followed a stamp a day ahead")
	}

	var f Field[string]
	if !f.Set("x", s1) || f.Set("x", s2) {
		t.Fatal("set")
	}
	if f.Merge(Field[string]{V: "old", S: Stamp{T: 1}}) || !f.Merge(Field[string]{V: "new", S: s2}) || f.V != "new" {
		t.Fatalf("merge %+v", f)
	}
	// Equal stamps: the larger value wins, whichever side merges.
	a, b := Field[string]{V: "a", S: s2}, Field[string]{V: "b", S: s2}
	a.Merge(b)
	b.Merge(Field[string]{V: "a", S: s2})
	if a.V != "b" || b.V != "b" {
		t.Fatalf("tie %q %q", a.V, b.V)
	}
}
