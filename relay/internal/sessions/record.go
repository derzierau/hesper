package sessions

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Shared history between Macs, the way projects are shared
// (internal/projects): every field group of an entry is a
// last-writer-wins register stamped with a hybrid logical clock. The
// home (the Mac whose CLI wrote the transcript) owns Meta, everything
// read from the transcript; any Mac may set Archived, Deleted (a
// tombstone), MovedTo and RemovedAt; each Mac sets its own entry in
// Mirrors. Equal stamps compare the values, so every order of merges
// converges.

// Stamp is a hybrid logical clock reading.
type Stamp struct {
	T int64  `json:"t,omitempty"` // Unix ms
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

func (s Stamp) String() string {
	if s.T == 0 && s.N == "" {
		return ""
	}
	return strconv.FormatInt(s.T, 10) + "." + strconv.Itoa(s.C) + "." + s.N
}

func parseStamp(v string) Stamp {
	if v == "" {
		return Stamp{}
	}
	parts := strings.SplitN(v, ".", 3)
	if len(parts) != 3 {
		return Stamp{}
	}
	t, _ := strconv.ParseInt(parts[0], 10, 64)
	c, _ := strconv.Atoi(parts[1])
	return Stamp{T: t, C: c, N: parts[2]}
}

// hlc is a hybrid logical clock.
type hlc struct {
	mu   sync.Mutex
	node string
	now  func() time.Time
	last Stamp
}

func (h *hlc) tick() Stamp {
	h.mu.Lock()
	defer h.mu.Unlock()
	t := h.now().UnixMilli()
	if t > h.last.T {
		h.last = Stamp{T: t}
	} else {
		h.last.C++
	}
	return Stamp{T: h.last.T, C: h.last.C, N: h.node}
}

// observe moves the clock past a stamp seen from another node (one more
// than a day ahead is not followed).
func (h *hlc) observe(s Stamp) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s.T > h.now().Add(24*time.Hour).UnixMilli() {
		return
	}
	if s.T > h.last.T || s.T == h.last.T && s.C > h.last.C {
		h.last.T, h.last.C = s.T, s.C
	}
}

type (
	wireLive = wire.SessionLive
	wireTodo = wire.SessionTodo
)

// Meta is what the home reads from a transcript (one register).
type Meta struct {
	Cwd           string             `json:"cwd,omitempty"`
	ProjectID     string             `json:"project,omitempty"`
	Branch        string             `json:"branch,omitempty"`
	Title         string             `json:"title,omitempty"`
	FirstPrompt   string             `json:"first,omitempty"`
	LastUser      string             `json:"lastUser,omitempty"`
	LastAssistant string             `json:"lastAssistant,omitempty"`
	Todos         []wire.SessionTodo `json:"todos,omitempty"`
	Turns         int                `json:"turns,omitempty"`
	Tokens        int64              `json:"tokens,omitempty"`
	StartedAt     int64              `json:"started,omitempty"`
	LastActivity  int64              `json:"last,omitempty"`
	Origin        string             `json:"origin,omitempty"`
	External      bool               `json:"external,omitempty"`
	Live          *wire.SessionLive  `json:"live,omitempty"`
	// Size and Version (size and mtime) of the transcript on the home;
	// Path is where it is there, UserHome the home's user folder (paths
	// under it map to another Mac's).
	Size     int64  `json:"size,omitempty"`
	Version  string `json:"version,omitempty"`
	Path     string `json:"path,omitempty"`
	UserHome string `json:"userHome,omitempty"`
	// Prompts and Answers are the searchable text (pieces joined with
	// pieceSep).
	Prompts string `json:"prompts,omitempty"`
	Answers string `json:"answers,omitempty"`
}

// Record is one entry of the shared history as stored and replicated.
type Record struct {
	Node string `json:"node"` // the home's node id
	Home string `json:"home"` // the home's own short name
	Kind string `json:"kind"`
	SID  string `json:"sid"`

	Meta      Meta   `json:"meta"`
	MetaS     Stamp  `json:"metaS"`
	Archived  bool   `json:"archived,omitempty"`
	ArchivedS Stamp  `json:"archivedS"`
	Deleted   bool   `json:"deleted,omitempty"`
	DeletedS  Stamp  `json:"deletedS"`
	MovedTo   string `json:"movedTo,omitempty"` // a node id
	MovedS    Stamp  `json:"movedS"`
	RemovedAt int64  `json:"removedAt,omitempty"` // Unix ms
	RemovedS  Stamp  `json:"removedS"`
	// Mirrors: node → when its mirror of the transcript was complete
	// (Unix ms; 0: dropped). Each node writes its own.
	Mirrors map[string]MirrorMark `json:"mirrors,omitempty"`
}

// MirrorMark is one node's mirror register.
type MirrorMark struct {
	At int64 `json:"at"`
	S  Stamp `json:"s"`
}

// Key is the record's id across Macs: "<node>:<kind>:<sid>".
func (r *Record) Key() string { return r.Node + ":" + r.Kind + ":" + r.SID }

func splitKey(key string) (node, kind, sid string, ok bool) {
	parts := strings.SplitN(key, ":", 3)
	if len(parts) != 3 || parts[0] == "" || parts[2] == "" || (parts[1] != wire.KindClaude && parts[1] != wire.KindCodex) {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

func newer(a Stamp, av any, b Stamp, bv any) bool {
	if a.Less(b) {
		return true
	}
	if a == b {
		x, _ := json.Marshal(av)
		y, _ := json.Marshal(bv)
		return bytes.Compare(x, y) < 0
	}
	return false
}

// merge folds o (another copy of the same entry) into r.
func (r *Record) merge(o *Record) (changed bool) {
	if newer(r.MetaS, r.Meta, o.MetaS, o.Meta) {
		r.Meta, r.MetaS, changed = o.Meta, o.MetaS, true
		if o.Home != "" {
			r.Home = o.Home
		}
	}
	if newer(r.ArchivedS, r.Archived, o.ArchivedS, o.Archived) {
		r.Archived, r.ArchivedS, changed = o.Archived, o.ArchivedS, true
	}
	if newer(r.DeletedS, r.Deleted, o.DeletedS, o.Deleted) {
		r.Deleted, r.DeletedS, changed = o.Deleted, o.DeletedS, true
	}
	if newer(r.MovedS, r.MovedTo, o.MovedS, o.MovedTo) {
		r.MovedTo, r.MovedS, changed = o.MovedTo, o.MovedS, true
	}
	if newer(r.RemovedS, r.RemovedAt, o.RemovedS, o.RemovedAt) {
		r.RemovedAt, r.RemovedS, changed = o.RemovedAt, o.RemovedS, true
	}
	for node, m := range o.Mirrors {
		cur, ok := r.Mirrors[node]
		if !ok || newer(cur.S, cur.At, m.S, m.At) {
			if r.Mirrors == nil {
				r.Mirrors = map[string]MirrorMark{}
			}
			r.Mirrors[node] = m
			changed = true
		}
	}
	return changed
}

func (r *Record) stamps(fn func(Stamp)) {
	fn(r.MetaS)
	fn(r.ArchivedS)
	fn(r.DeletedS)
	fn(r.MovedS)
	fn(r.RemovedS)
	for _, m := range r.Mirrors {
		fn(m.S)
	}
}

// valid: what a peer sends is sane.
func (r *Record) valid() bool {
	if r.Node == "" || len(r.Node) > 64 || strings.Contains(r.Node, ":") || len(r.Home) > 64 {
		return false
	}
	if r.Kind != wire.KindClaude && r.Kind != wire.KindCodex {
		return false
	}
	if !sidRE.MatchString(r.SID) || len(r.Mirrors) > 64 {
		return false
	}
	m := &r.Meta
	return len(m.Prompts) <= 4*maxPrompts && len(m.Answers) <= 4*maxAnswers && len(m.Title) < 4*maxTitle &&
		len(m.FirstPrompt) < 16*maxCard && len(m.LastUser) < 16*maxCard && len(m.LastAssistant) < 16*maxCard && len(m.Todos) <= maxTodos &&
		len(m.Path) < 4096 && len(m.Cwd) < 4096
}

func msTime(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339Nano)
}

func (r *Record) String() string { return fmt.Sprintf("%s (%s)", r.Key(), r.Meta.Title) }
