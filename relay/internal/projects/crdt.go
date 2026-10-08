package projects

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Shared data between Macs: every field of a project or group is a
// last-writer-wins register stamped with a hybrid logical clock (wall
// milliseconds, a counter, the writing node). Merging takes the newer
// stamp per field; equal stamps (two machines writing the same default)
// fall back to comparing the values, so every machine converges to the
// same state whatever order changes arrive in. Removal is a field too
// (Deleted, a tombstone), so a removed project stays removed when an
// older copy arrives, and a later promote revives it.

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

// hlc is a hybrid logical clock.
type hlc struct {
	node string
	now  func() time.Time
	last Stamp
}

func (h *hlc) tick() Stamp {
	t := h.now().UnixMilli()
	if t > h.last.T {
		h.last = Stamp{T: t}
	} else {
		h.last.C++
	}
	return Stamp{T: h.last.T, C: h.last.C, N: h.node}
}

// observe moves the clock past a stamp seen from another node (a stamp
// more than a day ahead of this clock is not followed).
func (h *hlc) observe(s Stamp) {
	if s.T > h.now().Add(24*time.Hour).UnixMilli() {
		return
	}
	if s.T > h.last.T || s.T == h.last.T && s.C > h.last.C {
		h.last.T, h.last.C = s.T, s.C
	}
}

// Field is a last-writer-wins register.
type Field[T any] struct {
	V T     `json:"v"`
	S Stamp `json:"s"`
}

// merge takes o when it is newer (or equal-stamped with a larger value).
func (f *Field[T]) merge(o Field[T]) bool {
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

// set writes a value with a stamp when it differs (changed: it did).
func (f *Field[T]) set(v T, s Stamp) bool {
	a, _ := json.Marshal(f.V)
	b, _ := json.Marshal(v)
	if bytes.Equal(a, b) {
		return false
	}
	f.V, f.S = v, s
	return true
}

// Record is a project as stored and replicated. Identity (and so ID) never
// changes; Paths are per node (a machine's daemon), each its own register.
type Record struct {
	ID       string                      `json:"id"`
	Identity wire.ProjectIdentity        `json:"identity"`
	ParentID string                      `json:"parentId,omitempty"`
	Name     Field[string]               `json:"name"`
	Color    Field[string]               `json:"color"`
	Kind     Field[string]               `json:"kind"`
	Defaults Field[wire.ProjectDefaults] `json:"defaults"`
	Deleted  Field[bool]                 `json:"deleted"`
	Paths    map[string]Field[string]    `json:"paths,omitempty"`
	// LastUsed only grows (the newest wins).
	LastUsed time.Time `json:"lastUsed,omitempty"`
	// Created (scratch projects) only shrinks (the oldest wins).
	Created time.Time `json:"created,omitzero"`
	// Scratch is a scratch project's lifecycle (scratch.go); it stays
	// when a promote makes the project a repository (Kind decides).
	Scratch *ScratchRecord `json:"scratch,omitempty"`
}

func (r *Record) clone() *Record {
	c := *r
	c.Paths = make(map[string]Field[string], len(r.Paths))
	for k, v := range r.Paths {
		c.Paths[k] = v
	}
	if r.Scratch != nil {
		sc := *r.Scratch
		c.Scratch = &sc
	}
	return &c
}

// merge folds another copy of the same project in.
func (r *Record) merge(o *Record) (changed, paths bool) {
	if r.ParentID == "" && o.ParentID != "" {
		r.ParentID, changed = o.ParentID, true
	}
	changed = r.Name.merge(o.Name) || changed
	changed = r.Color.merge(o.Color) || changed
	changed = r.Kind.merge(o.Kind) || changed
	changed = r.Defaults.merge(o.Defaults) || changed
	if r.Deleted.merge(o.Deleted) {
		changed, paths = true, true
	}
	if r.Paths == nil {
		r.Paths = map[string]Field[string]{}
	}
	for node, f := range o.Paths {
		cur, ok := r.Paths[node]
		if !ok {
			r.Paths[node] = f
			changed, paths = true, true
			continue
		}
		if cur.merge(f) {
			r.Paths[node] = cur
			changed, paths = true, true
		}
	}
	if o.LastUsed.After(r.LastUsed) {
		r.LastUsed, changed = o.LastUsed, true
	}
	if !o.Created.IsZero() && (r.Created.IsZero() || o.Created.Before(r.Created)) {
		r.Created, changed = o.Created, true
	}
	if o.Scratch != nil {
		if r.Scratch == nil {
			sc := *o.Scratch
			r.Scratch, changed, paths = &sc, true, true
		} else if r.Scratch.merge(o.Scratch) {
			changed, paths = true, true
		}
	}
	return changed, paths
}

func (r *Record) stamps(fn func(Stamp)) {
	fn(r.Name.S)
	fn(r.Color.S)
	fn(r.Kind.S)
	fn(r.Defaults.S)
	fn(r.Deleted.S)
	for _, f := range r.Paths {
		fn(f.S)
	}
	if r.Scratch != nil {
		r.Scratch.stamps(fn)
	}
}

// GroupRecord is a group as stored and replicated.
type GroupRecord struct {
	ID         string          `json:"id"`
	Name       Field[string]   `json:"name"`
	ProjectIDs Field[[]string] `json:"projectIds"`
	Order      Field[int]      `json:"order"`
	Color      Field[string]   `json:"color"`
	Deleted    Field[bool]     `json:"deleted"`
}

func (g *GroupRecord) merge(o *GroupRecord) bool {
	changed := g.Name.merge(o.Name)
	changed = g.ProjectIDs.merge(o.ProjectIDs) || changed
	changed = g.Order.merge(o.Order) || changed
	changed = g.Color.merge(o.Color) || changed
	changed = g.Deleted.merge(o.Deleted) || changed
	return changed
}

func (g *GroupRecord) stamps(fn func(Stamp)) {
	fn(g.Name.S)
	fn(g.ProjectIDs.S)
	fn(g.Order.S)
	fn(g.Color.S)
	fn(g.Deleted.S)
}

// State is what daemons exchange: everything shared (tombstones
// included), from Node, which calls itself Short. Nodes are the other
// nodes' own short names as far as the sender knows them.
type State struct {
	Node     string            `json:"node"`
	Short    string            `json:"short,omitempty"`
	Nodes    map[string]string `json:"nodes,omitempty"`
	Projects []*Record         `json:"projects"`
	Groups   []*GroupRecord    `json:"groups"`
}
