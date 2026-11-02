package projects

import (
	"time"

	"github.com/derzierau/hesper/relay/internal/crdt"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Shared data between Macs: every field of a project or group is a
// last-writer-wins register (internal/crdt). Removal is a field too
// (Deleted, a tombstone), so a removed project stays removed when an
// older copy arrives, and a later promote revives it.

// Stamp and Field are internal/crdt's.
type (
	Stamp        = crdt.Stamp
	Field[T any] = crdt.Field[T]
)

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
	changed = r.Name.Merge(o.Name) || changed
	changed = r.Color.Merge(o.Color) || changed
	changed = r.Kind.Merge(o.Kind) || changed
	changed = r.Defaults.Merge(o.Defaults) || changed
	if r.Deleted.Merge(o.Deleted) {
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
		if cur.Merge(f) {
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
	changed := g.Name.Merge(o.Name)
	changed = g.ProjectIDs.Merge(o.ProjectIDs) || changed
	changed = g.Order.Merge(o.Order) || changed
	changed = g.Color.Merge(o.Color) || changed
	changed = g.Deleted.Merge(o.Deleted) || changed
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
