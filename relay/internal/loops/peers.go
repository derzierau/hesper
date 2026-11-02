package loops

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Sync cursors (internal/remote): how far this Mac pulled from and pushed
// to each other Mac's store, kept next to loops.json in loops.peers.json
// (0600, atomic, written with it), so a restart does not pull and push
// everything again. They belong to this store's epoch: a new store here
// starts without them.

// Peer is what this Mac knows of one other node's loops.
type Peer struct {
	Epoch  string `json:"epoch,omitempty"`  // its epoch as last pulled
	Pulled int64  `json:"pulled,omitempty"` // its sequence number pulled up to
	Pushed int64  `json:"pushed,omitempty"` // this Mac's sequence number pushed up to
	Mine   string `json:"mine,omitempty"`   // this Mac's epoch when pushed
	Seen   string `json:"seen,omitempty"`   // its epoch in its last hint
	// Gen counts the resets of Pushed (PeerSeen): a push begun before one
	// does not record its cursor (PeerPushed).
	Gen int `json:"-"`
}

type peersFile struct {
	Version int              `json:"version"`
	Epoch   string           `json:"epoch"` // this store's
	Peers   map[string]*Peer `json:"peers"`
}

func (s *Store) peersPath() string {
	if s.path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(s.path), "loops.peers.json")
}

// loadPeers reads the cursors of this store's epoch (Open).
func (s *Store) loadPeers() {
	s.peers = map[string]*Peer{}
	path := s.peersPath()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.opt.Logf("hesperd: loops.peers.json: %v", err)
		}
		return
	}
	var f peersFile
	if err := json.Unmarshal(data, &f); err != nil {
		s.opt.Logf("hesperd: loops.peers.json: %v (pulling everything again)", err)
		return
	}
	if f.Epoch != s.epoch {
		return // another store's: this one starts over
	}
	for node, p := range f.Peers {
		if p != nil {
			s.peers[node] = p
		}
	}
}

// peersLocked copies the cursors when they changed (nil: unchanged),
// taken with the records write writes, so the file never holds cursors
// past the records loops.json holds.
func (s *Store) peersLocked() *peersFile {
	if s.peersPath() == "" || !s.peersDirty {
		return nil
	}
	s.peersDirty = false
	f := &peersFile{Version: 1, Epoch: s.epoch, Peers: make(map[string]*Peer, len(s.peers))}
	for node, p := range s.peers {
		c := *p
		f.Peers[node] = &c
	}
	return f
}

// writePeers writes cursors f (write, wmu held; nil: nothing).
func (s *Store) writePeers(f *peersFile) {
	if f == nil {
		return
	}
	path := s.peersPath()
	data, err := json.MarshalIndent(f, "", "  ")
	if err == nil {
		err = atomicWrite(path, append(data, '\n'), 0o600)
	}
	if err != nil {
		s.opt.Logf("hesperd: saving loops.peers.json: %v", err)
		s.mu.Lock()
		s.peersDirty = true
		s.mu.Unlock()
	}
}

func (s *Store) peerLocked(node string) *Peer {
	p := s.peers[node]
	if p == nil {
		p = &Peer{}
		s.peers[node] = p
	}
	return p
}

// Peer is node's cursors.
func (s *Store) Peer(node string) Peer {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.peers[node]; p != nil {
		return *p
	}
	return Peer{}
}

// PeerSeen records node's epoch from its hint and reports whether it is
// new: then node has a new store and gets everything again (Pushed 0).
func (s *Store) PeerSeen(node, epoch string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.peerLocked(node)
	if p.Seen == epoch {
		return false
	}
	p.Seen, p.Pushed = epoch, 0
	p.Gen++
	s.peersChangedLocked()
	return true
}

// PeerPulled records a pull from node up to next of its epoch.
func (s *Store) PeerPulled(node, epoch string, next int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.peerLocked(node)
	if p.Epoch != epoch || p.Pulled != next {
		p.Epoch, p.Pulled = epoch, next
		s.peersChangedLocked()
	}
}

// PeerPushed records a push to node up to next of this Mac's epoch mine,
// begun at gen (Peer().Gen): false when node was reset since (the push
// starts again from Peer()).
func (s *Store) PeerPushed(node string, gen int, next int64, mine string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.peerLocked(node)
	if p.Gen != gen {
		return false
	}
	if p.Pushed != next || p.Mine != mine {
		p.Pushed, p.Mine = next, mine
		s.peersChangedLocked()
	}
	return true
}

func (s *Store) peersChangedLocked() {
	s.peersDirty = true
	s.scheduleSave()
}
