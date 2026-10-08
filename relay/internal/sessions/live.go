package sessions

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/derzierau/hesper/relay/internal/agents"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Live detection: a session runs now when
//   - one of hesperd's agents runs it (agent.sessionId; live.agentId);
//   - a CLI process holds it: Claude's ~/.claude/sessions/<pid>.json
//     names it and that process is alive; Codex holds
//     ~/.codex/thread-writer-locks/<id>.lock (flock);
//   - its transcript was written within the last two minutes by another
//     process.
// The last two are live.external.

// recentWrite: a transcript written this recently is in use.
const recentWrite = 2 * time.Minute

var claudePids = struct {
	sync.Mutex
	at   time.Time
	dir  string
	byID map[string]int
}{}

// claudeLive reports whether a live Claude process names the session.
func (s *Service) claudeLive(sid string) bool {
	claudePids.Lock()
	defer claudePids.Unlock()
	dir := filepath.Join(s.opt.ClaudeHome, "sessions")
	if claudePids.dir != dir || time.Since(claudePids.at) > 2*time.Second {
		claudePids.dir, claudePids.at, claudePids.byID = dir, time.Now(), map[string]int{}
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil || len(data) > 1<<20 {
				continue
			}
			var v struct {
				PID       int    `json:"pid"`
				SessionID string `json:"sessionId"`
			}
			if json.Unmarshal(data, &v) == nil && v.PID > 0 && v.SessionID != "" {
				claudePids.byID[v.SessionID] = v.PID
			}
		}
	}
	pid := claudePids.byID[sid]
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// codexLive reports whether a Codex process holds the session's writer
// lock (tested with a shared, non-blocking flock that is let go at once).
func (s *Service) codexLive(sid string) bool {
	f, err := os.Open(filepath.Join(s.opt.CodexHome, "thread-writer-locks", sid+".lock"))
	if err != nil {
		return false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		return errors.Is(err, syscall.EWOULDBLOCK)
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

// liveOf is a session's live state (mtime: its transcript's, ns).
func (s *Service) liveOf(kind, sid string, mtime int64) *wire.SessionLive {
	if reg := s.registry(); reg != nil {
		for _, a := range reg.List() {
			if a.Kind == kind && a.SessionID == sid {
				if a.Exit == nil && a.State != wire.StateExited {
					return &wire.SessionLive{AgentID: a.ID}
				}
				return nil // hesperd's, ended: its last writes were its own
			}
		}
	}
	switch kind {
	case wire.KindClaude:
		if s.claudeLive(sid) {
			return &wire.SessionLive{External: true}
		}
	case wire.KindCodex:
		// Only sessions written within a day are asked (the check takes
		// the lock for a moment when it is free).
		if (mtime == 0 || s.opt.Now().Sub(time.Unix(0, mtime)) < 24*time.Hour) && s.codexLive(sid) {
			return &wire.SessionLive{External: true}
		}
	}
	// A recent write of a session hesperd ran (its agent gone) was its
	// agent's; others are another process's.
	if mtime > 0 && !s.isOwned(kind, sid) && s.opt.Now().Sub(time.Unix(0, mtime)) < recentWrite {
		return &wire.SessionLive{External: true}
	}
	return nil
}

func sameLive(a, b *wire.SessionLive) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// refreshLive re-checks this Mac's sessions that are live or were active
// lately (a process ends without writing).
func (s *Service) refreshLive() {
	cutoff := s.opt.Now().Add(-10 * time.Minute).UnixMilli()
	rows, err := s.db.r.Query(`SELECT kind, sid, path FROM sessions WHERE node = ? AND deleted = 0 AND (live != '' OR last > ?)`, s.db.node, cutoff)
	if err != nil {
		return
	}
	type item struct{ kind, sid, path string }
	var items []item
	for rows.Next() {
		var it item
		if rows.Scan(&it.kind, &it.sid, &it.path) == nil {
			items = append(items, it)
		}
	}
	rows.Close()
	for _, it := range items {
		var mtime int64
		if st, err := os.Stat(it.path); err == nil {
			mtime = st.ModTime().UnixNano()
		}
		live := s.liveOf(it.kind, it.sid, mtime)
		s.updateLocal(it.kind, it.sid, func(r *Record) bool {
			if sameLive(r.Meta.Live, live) {
				return false
			}
			r.Meta.Live = live
			return true
		})
	}
}

// busy: one of this Mac's agents is working (the indexer slows down).
func (s *Service) busy() bool {
	if s.opt.Busy != nil {
		return s.opt.Busy()
	}
	reg := s.registry()
	if reg == nil {
		return false
	}
	for _, a := range reg.List() {
		if a.State == wire.StateWorking || a.State == wire.StateStarting {
			return true
		}
	}
	return false
}

// watchAgents follows the registry: sessions hesperd starts are its own
// (not external), their live state follows the agent, and an agent's
// removal leaves its session in the history with removedAt.
func (s *Service) watchAgents(reg *agents.Registry) {
	sub := reg.Subscribe()
	defer reg.Unsubscribe(sub)
	known := map[string]wire.Agent{}
	for {
		notes := sub.Wait(s.stop)
		if notes == nil {
			return
		}
		for _, n := range notes {
			if n.Agent != nil {
				a := *n.Agent
				prev := known[a.ID]
				known[a.ID] = a
				if a.SessionID == "" {
					continue
				}
				s.own(a.Kind, a.SessionID)
				if prev.SessionID != a.SessionID || (prev.Exit == nil) != (a.Exit == nil) || prev.State != a.State {
					live := s.liveOf(a.Kind, a.SessionID, 0)
					s.updateLocal(a.Kind, a.SessionID, func(r *Record) bool {
						if sameLive(r.Meta.Live, live) {
							return false
						}
						r.Meta.Live = live
						return true
					})
				}
				if a.State == wire.StateDone || a.State == wire.StateIdle || a.Exit != nil {
					s.scan.poke()
				}
				continue
			}
			a, ok := known[n.Removed]
			delete(known, n.Removed)
			if !ok || a.SessionID == "" {
				continue
			}
			s.agentRemoved(a)
		}
	}
}

// agentRemoved marks a removed agent's session (ghost cards).
func (s *Service) agentRemoved(a wire.Agent) {
	key := s.db.node + ":" + a.Kind + ":" + a.SessionID
	at := s.opt.Now().UnixMilli()
	s.edit(key, func(r *Record, st Stamp) bool {
		r.RemovedAt, r.RemovedS = at, st
		return true
	})
	s.updateLocal(a.Kind, a.SessionID, func(r *Record) bool {
		if r.Meta.Live == nil {
			return false
		}
		r.Meta.Live = nil
		return true
	})
}

// watchProjects maps this Mac's sessions to projects again when the
// projects change (promoted, removed, merged from another Mac).
func (s *Service) watchProjects(p Projects) {
	for {
		select {
		case <-s.stop:
			return
		case <-p.Changes():
		}
		select {
		case <-s.stop:
			return
		case <-time.After(2 * time.Second):
		}
		s.scan.mu.Lock()
		s.scan.cwdProj = map[string]string{}
		s.scan.mu.Unlock()
		rows, err := s.db.r.Query(`SELECT kind, sid, cwd, project FROM sessions WHERE node = ? AND deleted = 0`, s.db.node)
		if err != nil {
			continue
		}
		type item struct{ kind, sid, cwd, project string }
		var items []item
		for rows.Next() {
			var it item
			if rows.Scan(&it.kind, &it.sid, &it.cwd, &it.project) == nil {
				items = append(items, it)
			}
		}
		rows.Close()
		for _, it := range items {
			id := s.scan.project(it.cwd)
			if id == it.project {
				continue
			}
			s.updateLocal(it.kind, it.sid, func(r *Record) bool {
				if r.Meta.ProjectID == id {
					return false
				}
				r.Meta.ProjectID = id
				return true
			})
		}
	}
}
