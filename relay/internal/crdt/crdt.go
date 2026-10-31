// Package crdt holds the replicated registers the daemons of the owner's
// Macs share (projects, loops): every field is a last-writer-wins
// register stamped with a hybrid logical clock (wall milliseconds, a
// counter, the writing node). Merging takes the newer stamp per field;
// equal stamps (two machines writing the same default) fall back to
// comparing the values, so every machine converges to the same state
// whatever order changes arrive in. Removal is a field too (a tombstone),
// so a removed record stays removed when an older copy arrives.
package crdt

import (
	"bytes"
	"encoding/json"
	"time"
)

// Stamp is a hybrid logical clock reading.
type Stamp struct {
	T int64  `json:"t,omitempty"` // Unix milliseconds
	C int    `json:"c,omitempty"`
	N string `json:"n,omitempty"` // node
}

// Less orders stamps: time, counter, node.
func (s Stamp) Less(o Stamp) bool {
	if s.T != o.T {
		return s.T < o.T
	}
	if s.C != o.C {
		return s.C < o.C
	}
	return s.N < o.N
}

// Clock is a hybrid logical clock of one node. Not safe for concurrent
// use: its owner's lock guards it.
type Clock struct {
	Node string
	Now  func() time.Time
	Last Stamp
}

// Tick is the stamp of a write now.
func (h *Clock) Tick() Stamp {
	t := h.Now().UnixMilli()
	if t > h.Last.T {
		h.Last = Stamp{T: t}
	} else {
		h.Last.C++
	}
	return Stamp{T: h.Last.T, C: h.Last.C, N: h.Node}
}

// Observe moves the clock past a stamp seen from another node (a stamp
// more than a day ahead of this clock is not followed).
func (h *Clock) Observe(s Stamp) {
	if s.T > h.Now().Add(24*time.Hour).UnixMilli() {
		return
	}
	if s.T > h.Last.T || s.T == h.Last.T && s.C > h.Last.C {
		h.Last.T, h.Last.C = s.T, s.C
	}
}

// Field is a last-writer-wins register.
type Field[T any] struct {
	V T     `json:"v"`
	S Stamp `json:"s"`
}

// Merge takes o when it is newer (or equal-stamped with a larger value).
func (f *Field[T]) Merge(o Field[T]) bool {
	if f.S.Less(o.S) {
		*f = o
		return true
	}
	if f.S == o.S {
		a, _ := json.Marshal(f.V)
		b, _ := json.Marshal(o.V)
		if bytes.Compare(a, b) < 0 {
			*f = o
			return true
		}
	}
	return false
}

// Set writes a value with a stamp when it differs (changed: it did).
func (f *Field[T]) Set(v T, s Stamp) bool {
	a, _ := json.Marshal(f.V)
	b, _ := json.Marshal(v)
	if bytes.Equal(a, b) {
		return false
	}
	f.V, f.S = v, s
	return true
}
