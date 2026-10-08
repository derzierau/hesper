package sessions

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Checkpoints (move work): the last checkpoint of the hesperd agent that
// ran a session of this Mac (internal/agents/checkpoint.go) stays with
// the session (Session.checkpoint), also once the agent is gone. They are
// this Mac's only (the refs live in its repositories) and kept in the
// index's checkpoints table, not replicated. checkpoints.restore makes a
// worktree from one.

// checkpointOf is a session's checkpoint (nil: none).
func (s *Service) checkpointOf(kind, sid string) *wire.Checkpoint {
	s.cpMu.Lock()
	defer s.cpMu.Unlock()
	if s.checkpoints == nil {
		s.checkpoints = map[string]wire.Checkpoint{}
		if rows, err := s.db.r.Query(`SELECT kind, sid, data FROM checkpoints`); err == nil {
			for rows.Next() {
				var k, id, data string
				var cp wire.Checkpoint
				if rows.Scan(&k, &id, &data) == nil && json.Unmarshal([]byte(data), &cp) == nil {
					s.checkpoints[k+":"+id] = cp
				}
			}
			rows.Close()
		}
	}
	cp, ok := s.checkpoints[kind+":"+sid]
	if !ok {
		return nil
	}
	return &cp
}

// agentCheckpoint keeps an agent's new checkpoint with its session.
func (s *Service) agentCheckpoint(a wire.Agent) {
	if a.Checkpoint == nil || a.SessionID == "" || !sidRE.MatchString(a.SessionID) {
		return
	}
	if old := s.checkpointOf(a.Kind, a.SessionID); old != nil && *old == *a.Checkpoint {
		return
	}
	cp := *a.Checkpoint
	s.cpMu.Lock()
	s.checkpoints[a.Kind+":"+a.SessionID] = cp
	s.cpMu.Unlock()
	data, _ := json.Marshal(cp)
	s.db.write(func(tx sqlTx) error {
		_, err := tx.Exec(`INSERT OR REPLACE INTO checkpoints (kind, sid, data) VALUES (?, ?, ?)`, a.Kind, a.SessionID, string(data))
		return err
	})
	s.noteChange(s.db.node+":"+a.Kind+":"+a.SessionID, true)
}

// RestoreParams are checkpoints.restore's: the session whose checkpoint
// (ref, commit) to restore, on machine (default: the session's home).
type RestoreParams struct {
	Session string `json:"session"`
	Ref     string `json:"ref"`
	Commit  string `json:"commit"`
	Machine string `json:"machine,omitempty"`
}

// RestoreResult is checkpoints.restore's: the new worktree.
type RestoreResult struct {
	Path   string `json:"path"`
	Branch string `json:"branch,omitempty"`
}

// restoreCheckpoint runs checkpoints.restore: on the session's home (the
// Mac that has the checkpoint), a new worktree from the checkpoint on its
// branch with the uncommitted changes restored.
func (s *Service) restoreCheckpoint(p RestoreParams) (RestoreResult, error) {
	rw, err := s.find(p.Session)
	if err != nil {
		return RestoreResult{}, err
	}
	rec := &rw.rec
	home := s.nameOf(rec.Node, rec.Home)
	if p.Machine != "" && p.Machine != home {
		return RestoreResult{}, wire.Errorf(wire.CodeInvalid, "the checkpoint is on %s, the session's home", home)
	}
	if rec.Node != s.db.node {
		peers := s.peerSet()
		if peers == nil {
			return RestoreResult{}, wire.Errorf(wire.CodeUnavailable, "machine %s is not connected", home)
		}
		ctx, cancel := context.WithTimeout(s.ctx, 2*time.Minute)
		defer cancel()
		params, _ := json.Marshal(hostRestoreParams{Key: rec.Key(), Ref: p.Ref, Commit: p.Commit})
		raw, err := peers.Call(ctx, home, "checkpoints.restore", params)
		var res RestoreResult
		if err == nil {
			err = json.Unmarshal(raw, &res)
		}
		return res, err
	}
	return s.restoreHere(rec, p.Ref, p.Commit)
}

type hostRestoreParams struct {
	Key    string `json:"key"`
	Ref    string `json:"ref"`
	Commit string `json:"commit"`
}

func (s *Service) restoreHere(rec *Record, ref, commit string) (RestoreResult, error) {
	reg := s.registry()
	if reg == nil {
		return RestoreResult{}, wire.Errorf(wire.CodeUnavailable, "no agents")
	}
	if rec.Node != s.db.node {
		return RestoreResult{}, wire.Errorf(wire.CodeNotFound, "%s is not this Mac's", rec.Key())
	}
	branch := rec.Meta.Branch
	if cp := s.checkpointOf(rec.Kind, rec.SID); cp != nil {
		if ref == "" {
			ref = cp.Ref
		}
		if commit == "" {
			commit = cp.Commit
		}
		if cp.Branch != "" {
			branch = cp.Branch
		}
	}
	if commit == "" {
		return RestoreResult{}, wire.Errorf(wire.CodeNotFound, "session %s has no checkpoint", rec.Key())
	}
	ctx, cancel := context.WithTimeout(s.ctx, 2*time.Minute)
	defer cancel()
	dir := rec.Meta.Cwd
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		// Its worktree is gone: the project's folder here.
		if pr := s.projectReg(); pr != nil && rec.Meta.ProjectID != "" {
			if p := pr.PathOn(rec.Meta.ProjectID); p != "" {
				dir = p
			}
		}
	}
	path, used, err := reg.RestoreCheckpoint(ctx, dir, ref, commit, branch)
	if err != nil {
		return RestoreResult{}, err
	}
	return RestoreResult{Path: path, Branch: used}, nil
}
