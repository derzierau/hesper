package agents

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Drafts: new agents the app is still composing (draft tiles). The daemon
// only stores them (drafts.json, atomic writes) and tells subscribers;
// it never reads their text. Starting a draft is an ordinary agents.spawn
// by the app followed by drafts.remove.

const (
	draftLimit     = 200
	draftTextLimit = 256 << 10
)

var draftID = regexp.MustCompile(`^d-[a-z0-9-]{1,40}$`)

// DraftStore holds the drafts of this daemon's app.
type DraftStore struct {
	path string // "": in memory only
	logf func(string, ...any)

	mu     sync.Mutex
	drafts map[string]wire.Draft
	subs   map[*draftSub]struct{}
	wmu    sync.Mutex // serializes file writes
}

// OpenDrafts loads dir/drafts.json (dir "": memory only).
func OpenDrafts(dir string, logf func(string, ...any)) *DraftStore {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	s := &DraftStore{logf: logf, drafts: map[string]wire.Draft{}, subs: map[*draftSub]struct{}{}}
	if dir == "" {
		return s
	}
	s.path = filepath.Join(dir, "drafts.json")
	data, err := os.ReadFile(s.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logf("hesperd: drafts.json: %v", err)
		}
		return s
	}
	var f draftFile
	if err := json.Unmarshal(data, &f); err != nil {
		os.Rename(s.path, s.path+".broken")
		logf("hesperd: drafts.json: %v (moved to drafts.json.broken)", err)
		return s
	}
	for _, d := range f.Drafts {
		if draftID.MatchString(d.ID) {
			s.drafts[d.ID] = d
		}
	}
	return s
}

type draftFile struct {
	Version int          `json:"version"`
	Drafts  []wire.Draft `json:"drafts"`
}

// List is every draft, oldest first.
func (s *DraftStore) List() []wire.Draft {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listLocked()
}

func (s *DraftStore) listLocked() []wire.Draft {
	out := make([]wire.Draft, 0, len(s.drafts))
	for _, d := range s.drafts {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Created.Equal(out[j].Created) {
			return out[i].Created.Before(out[j].Created)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Save creates or replaces a draft; Created is kept from an existing one,
// Updated is now.
func (s *DraftStore) Save(d wire.Draft) (wire.Draft, error) {
	if d.ID == "" {
		d.ID = newDraftID()
	}
	if !draftID.MatchString(d.ID) {
		return wire.Draft{}, wire.Errorf(wire.CodeInvalid, "draft id %q: want d- and 1-40 of [a-z0-9-]", d.ID)
	}
	if len(d.Text) > draftTextLimit {
		return wire.Draft{}, wire.Errorf(wire.CodeInvalid, "draft text is over %d KiB", draftTextLimit>>10)
	}
	now := time.Now().UTC()
	s.mu.Lock()
	old, exists := s.drafts[d.ID]
	if !exists && len(s.drafts) >= draftLimit {
		s.mu.Unlock()
		return wire.Draft{}, wire.Errorf(wire.CodeInvalid, "too many drafts (%d)", draftLimit)
	}
	switch {
	case exists && !old.Created.IsZero():
		d.Created = old.Created
	case d.Created.IsZero():
		d.Created = now
	}
	d.Updated = now
	s.drafts[d.ID] = d
	s.notifyLocked(d.ID, &d)
	s.mu.Unlock()
	s.save()
	return d, nil
}

// Remove forgets a draft (not_found when there is none).
func (s *DraftStore) Remove(id string) error {
	s.mu.Lock()
	if _, ok := s.drafts[id]; !ok {
		s.mu.Unlock()
		return wire.Errorf(wire.CodeNotFound, "no draft %s", id)
	}
	delete(s.drafts, id)
	s.notifyLocked(id, nil)
	s.mu.Unlock()
	s.save()
	return nil
}

func (s *DraftStore) save() {
	if s.path == "" {
		return
	}
	// One writer at a time, each writing the latest list: the file never
	// goes back to an older state.
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.mu.Lock()
	f := draftFile{Version: 1, Drafts: s.listLocked()}
	s.mu.Unlock()
	data, err := json.MarshalIndent(f, "", "  ")
	if err == nil {
		err = atomicWrite(s.path, append(data, '\n'), 0o600)
	}
	if err != nil {
		s.logf("hesperd: saving drafts.json: %v", err)
	}
}

func newDraftID() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 8)
	rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return "d-" + string(b)
}

// call runs a drafts.* method.
func (s *DraftStore) call(method string, params json.RawMessage) (any, error) {
	switch method {
	case "drafts.list":
		return s.List(), nil
	case "drafts.save":
		var p wire.DraftSaveParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		return s.Save(p.Draft)
	case "drafts.remove":
		var p wire.IDParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		if p.ID == "" {
			return nil, &badParams{errors.New("id is required")}
		}
		return struct{}{}, s.Remove(p.ID)
	}
	return nil, &noMethod{method}
}

// Subscriptions: like agents', coalesced per draft (the latest wins).

type draftSub struct {
	mu      sync.Mutex
	pending map[string]*wire.Draft // nil: removed
	order   []string
	wake    chan struct{}
}

func (d *draftSub) push(id string, v *wire.Draft) {
	d.mu.Lock()
	if _, ok := d.pending[id]; !ok {
		d.order = append(d.order, id)
	}
	d.pending[id] = v
	d.mu.Unlock()
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

func (d *draftSub) take() (changed []wire.Draft, removed []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, id := range d.order {
		if v := d.pending[id]; v != nil {
			changed = append(changed, *v)
		} else {
			removed = append(removed, id)
		}
	}
	d.order, d.pending = nil, map[string]*wire.Draft{}
	return
}

func (s *DraftStore) notifyLocked(id string, d *wire.Draft) {
	for sub := range s.subs {
		var v *wire.Draft
		if d != nil {
			c := *d
			v = &c
		}
		sub.push(id, v)
	}
}

func (s *DraftStore) subscribe() *draftSub {
	sub := &draftSub{pending: map[string]*wire.Draft{}, wake: make(chan struct{}, 1)}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.listLocked() {
		c := d
		sub.push(d.ID, &c)
	}
	s.subs[sub] = struct{}{}
	return sub
}

func (s *DraftStore) unsubscribe(sub *draftSub) {
	s.mu.Lock()
	delete(s.subs, sub)
	s.mu.Unlock()
}

// subscribeDrafts sends drafts.changed / drafts.removed on a subscribed
// control connection until it closes.
func (c *ctrl) subscribeDrafts(wg *sync.WaitGroup) {
	store := c.s.drafts
	sub := store.subscribe()
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer store.unsubscribe(sub)
		for {
			changed, removed := sub.take()
			for _, d := range changed {
				params, _ := json.Marshal(wire.DraftChanged{Draft: d})
				if c.write(wire.Notification{JSONRPC: "2.0", Method: wire.NoteDraftChanged, Params: params}) != nil {
					return
				}
			}
			for _, id := range removed {
				params, _ := json.Marshal(wire.Removed{ID: id})
				if c.write(wire.Notification{JSONRPC: "2.0", Method: wire.NoteDraftRemoved, Params: params}) != nil {
					return
				}
			}
			select {
			case <-sub.wake:
			case <-c.done:
				return
			}
		}
	}()
}
