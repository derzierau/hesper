package sessions

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/derzierau/hesper/relay/internal/agents"
	"github.com/derzierau/hesper/relay/internal/handoff"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Actions: resume and fork (on the home, or moved / copied to another
// Mac), brief and continue in the other kind, archive, delete.
//
// Ownership: a session continued on another Mac moves there. Its
// transcript is placed there (from the home when it is reachable — with
// its uncommitted work, by the same handoff pack as agents.move — else
// from this Mac's mirror, on its branch as pushed), the agent resumes it,
// and the old entry gets movedTo: read-only history; resuming it goes to
// the new home. A fork leaves the original where it is.

// UndoWindow is how long a delete can be taken back before the
// transcript is removed on its home.
var UndoWindow = 30 * time.Second

// ResumeResult is sessions.resume's / sessions.fork's result: the agent,
// and a note when something did not come along (additive). Between Macs
// (transfer.go) a target still at work answers Pending (the operation
// to ask again) and its Progress instead.
type ResumeResult struct {
	wire.Agent
	Note     string        `json:"note,omitempty"`
	Pending  string        `json:"pending,omitempty"`
	Progress *wire.Moving  `json:"progress,omitempty"`
	Steps    []wire.Moving `json:"steps,omitempty"`
}

// Call runs a sessions method on the local socket.
func (s *Service) Call(method string, params json.RawMessage) (any, error, bool) {
	switch method {
	case "sessions.search":
		var p wire.SessionSearchParams
		if err := decode(params, &p); err != nil {
			return nil, err, true
		}
		res, err := s.Search(p)
		return res, err, true
	case "sessions.show":
		var p wire.SessionIDParams
		if err := decode(params, &p); err != nil {
			return nil, err, true
		}
		res, err := s.Show(p.ID)
		return res, err, true
	case "sessions.resume", "sessions.fork":
		var p struct {
			wire.SessionResumeParams
			treeParams
		}
		if err := decode(params, &p); err != nil {
			return nil, err, true
		}
		res, err := s.resume(p.ID, p.Machine, method == "sessions.fork", p.tree())
		return res, err, true
	case "sessions.brief":
		var p wire.SessionIDParams
		if err := decode(params, &p); err != nil {
			return nil, err, true
		}
		rw, err := s.find(p.ID)
		if err != nil {
			return nil, err, true
		}
		return wire.SessionBrief{Text: s.brief(&rw.rec)}, nil, true
	case "sessions.continueAs":
		var p struct {
			wire.SessionContinueParams
			treeParams
		}
		if err := decode(params, &p); err != nil {
			return nil, err, true
		}
		res, err := s.continueAs(p.ID, p.Kind, p.Machine, p.tree())
		return res, err, true
	case "sessions.archive":
		var p wire.SessionArchiveParams
		if err := decode(params, &p); err != nil {
			return nil, err, true
		}
		res, err := s.Archive(p.ID, p.Archived)
		return res, err, true
	case "sessions.delete":
		var p wire.SessionDeleteParams
		if err := decode(params, &p); err != nil {
			return nil, err, true
		}
		return struct{}{}, s.Delete(p.ID, p.Undo), true
	case "sessions.stats":
		res, err := s.Stats()
		return res, err, true
	case "checkpoints.restore":
		// move work (checkpoints.go)
		var p RestoreParams
		if err := decode(params, &p); err != nil {
			return nil, err, true
		}
		res, err := s.restoreCheckpoint(p)
		return res, err, true
	}
	return nil, nil, false
}

func decode(params json.RawMessage, v any) error {
	if len(params) == 0 || string(params) == "null" {
		return nil
	}
	if err := json.Unmarshal(params, v); err != nil {
		return wire.Errorf(wire.CodeInvalid, "invalid params: %v", err)
	}
	return nil
}

// treeParams (agent tree): the place in the tree of the agent a session
// start makes. hesperd's server sets them when an agent calls (the new
// agent is its child; a person's calls never carry them); a controller
// sends them to the host that starts it.
type treeParams struct {
	Parent string `json:"parent,omitempty"`
	Depth  int    `json:"depth,omitempty"`
}

// Tree is where in the agent tree a session start puts its agent.
type Tree struct {
	Parent string
	Depth  int
}

func (p treeParams) tree() Tree { return Tree{Parent: p.Parent, Depth: p.Depth} }

func (t Tree) params() treeParams { return treeParams{Parent: t.Parent, Depth: t.Depth} }

// hostParams are the host methods' params: entries are named by key.
// sessions.resume / sessions.fork (transfer.go): Poll, the caller follows
// a pending operation; Op, the operation it asks about (no key).
type hostParams struct {
	Key  string `json:"key"`
	Kind string `json:"kind,omitempty"`
	Op   string `json:"op,omitempty"`
	Poll bool   `json:"poll,omitempty"`
	Seen int    `json:"seen,omitempty"`
	treeParams
}

// HostCall answers the host methods another Mac's hesperd sends
// (internal/host): sessions.pull, sessions.transcript, sessions.plan,
// sessions.changes, sessions.resume / sessions.fork / sessions.continueAs
// here.
func (s *Service) HostCall(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "sessions.pull":
		var p pullParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		return s.pull(p)
	case "sessions.transcript":
		var p transcriptParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		return s.transcriptChunk(p)
	case "checkpoints.restore":
		// move work (checkpoints.go)
		var p hostRestoreParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		rw, err := s.db.get(p.Key)
		if err != nil {
			return nil, err
		}
		if rw == nil || rw.rec.Deleted {
			return nil, wire.Errorf(wire.CodeNotFound, "no session %s", p.Key)
		}
		return s.restoreHere(&rw.rec, p.Ref, p.Commit)
	}
	var p hostParams
	if err := decode(params, &p); err != nil {
		return nil, err
	}
	if p.Op != "" && (method == "sessions.resume" || method == "sessions.fork") {
		return s.pollOp(ctx, p.Op, p.Seen)
	}
	rw, err := s.db.get(p.Key)
	if err != nil {
		return nil, err
	}
	if rw == nil || rw.rec.Deleted {
		return nil, wire.Errorf(wire.CodeNotFound, "no session %s", p.Key)
	}
	rec := &rw.rec
	switch method {
	case "sessions.plan":
		if rec.Node != s.db.node {
			return nil, wire.Errorf(wire.CodeNotFound, "%s is not this Mac's", p.Key)
		}
		reg := s.registry()
		if reg == nil {
			return nil, wire.Errorf(wire.CodeUnavailable, "no agents")
		}
		return handoff.PlanFor(ctx, s.pseudoAgent(rec), reg.HandoffPaths()), nil
	case "sessions.changes":
		if rec.Node != s.db.node {
			return nil, wire.Errorf(wire.CodeNotFound, "%s is not this Mac's", p.Key)
		}
		return s.gitc.get(rec.Meta.Cwd, s.gitEnv()), nil
	case "sessions.resume", "sessions.fork":
		// Its own operation: it outlasts the request (transfer.go).
		return s.awaitOp(ctx, s.startOp(*rec, method == "sessions.fork", p.tree()), p.Poll, 0)
	case "sessions.continueAs":
		return s.continueHere(rec, p.Kind, p.tree())
	}
	return nil, wire.Errorf(wire.CodeNotFound, "no method %s", method)
}

func (s *Service) gitEnv() []string {
	if reg := s.registry(); reg != nil {
		return reg.HandoffPaths().Env
	}
	return nil
}

// Show runs sessions.show: the entry, and its git state on its home.
func (s *Service) Show(id string) (wire.SessionDetail, error) {
	rw, err := s.find(id)
	if err != nil {
		return wire.SessionDetail{}, err
	}
	if rw.rec.Deleted {
		return wire.SessionDetail{}, wire.Errorf(wire.CodeNotFound, "session %s was deleted", id)
	}
	full, err := s.db.get(rw.rec.Key())
	if err == nil && full != nil {
		rw = full
	}
	out := wire.SessionDetail{Session: s.toWire(&rw.rec)}
	out.Changes = s.changesOf(&rw.rec)
	return out, nil
}

func (s *Service) changesOf(r *Record) *wire.SessionChanges {
	if r.Meta.Cwd == "" {
		return nil
	}
	if r.Node == s.db.node {
		return s.gitc.get(r.Meta.Cwd, s.gitEnv())
	}
	peers := s.peerSet()
	if peers == nil || !linked(peers, s.nameOf(r.Node, r.Home)) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	params, _ := json.Marshal(hostParams{Key: r.Key()})
	raw, err := peers.Call(ctx, s.nameOf(r.Node, r.Home), "sessions.changes", params)
	if err != nil {
		return nil
	}
	var ch wire.SessionChanges
	if json.Unmarshal(raw, &ch) != nil {
		return nil
	}
	return &ch
}

func linked(p Peers, machine string) bool {
	for _, m := range p.Linked() {
		if m == machine {
			return true
		}
	}
	return false
}

// follow is the entry a session continued in (movedTo), else r.
func (s *Service) follow(rw *row) *row {
	for i := 0; i < 8 && rw.rec.MovedTo != ""; i++ {
		next, err := s.db.get(rw.rec.MovedTo + ":" + rw.rec.Kind + ":" + rw.rec.SID)
		if err != nil || next == nil || next.rec.Deleted {
			break
		}
		rw = next
	}
	return rw
}

// liveNow is a session's live state as far as this Mac knows: checked
// here for its own sessions, replicated for others'. Agent ids are in
// this Mac's naming.
func (s *Service) liveNow(r *Record) *wire.SessionLive {
	if r.Node == s.db.node {
		var mtime int64
		if st, err := os.Stat(r.Meta.Path); err == nil {
			mtime = st.ModTime().UnixNano()
		}
		return s.liveOf(r.Kind, r.SID, mtime)
	}
	if r.Meta.Live == nil {
		return nil
	}
	l := *r.Meta.Live
	if l.AgentID != "" {
		if _, local, ok := strings.Cut(l.AgentID, "/"); ok {
			l.AgentID = s.nameOf(r.Node, r.Home) + "/" + local
		}
	}
	return &l
}

func liveError(sid string, l *wire.SessionLive) error {
	msg := "session " + sid + " is running"
	if l.AgentID != "" {
		msg += " in " + l.AgentID
	} else {
		msg += " in another program"
	}
	return &wire.Error{Code: wire.CodeLive, Message: msg, AgentID: l.AgentID}
}

// Resume runs sessions.resume / sessions.fork: on machine (default: the
// session's home).
func (s *Service) Resume(id, machine string, fork bool) (ResumeResult, error) {
	return s.resume(id, machine, fork, Tree{})
}

// resume is Resume with the new agent's place in the agent tree.
func (s *Service) resume(id, machine string, fork bool, tree Tree) (ResumeResult, error) {
	rw, err := s.find(id)
	if err != nil {
		return ResumeResult{}, err
	}
	if !fork {
		rw = s.follow(rw)
	}
	rec := &rw.rec
	if rec.Deleted {
		return ResumeResult{}, wire.Errorf(wire.CodeNotFound, "session %s was deleted", id)
	}
	if !fork {
		if l := s.liveNow(rec); l != nil {
			return ResumeResult{}, liveError(rec.SID, l)
		}
	}
	target := machine
	if target == "" {
		target = s.nameOf(rec.Node, rec.Home)
	}
	if target == s.machine() {
		ctx, cancel := context.WithTimeout(s.ctx, TransferLimit)
		defer cancel()
		var progress func(wire.Moving)
		if reg := s.registry(); reg != nil && rec.Node != s.db.node {
			// Brought here: this Mac's subscribers see it come.
			sid := s.nameOf(rec.Node, rec.Home) + ":" + rec.Kind + ":" + rec.SID
			progress = func(m wire.Moving) {
				m.ID, m.To, m.Fork, m.Session = sid, target, fork, true
				reg.NoteMoving(m)
			}
		}
		res, err := s.resumeHere(ctx, rec, fork, tree, progress)
		if progress != nil {
			if err != nil {
				var we *wire.Error
				if !errors.As(err, &we) {
					we = &wire.Error{Code: wire.CodeRemote, Message: err.Error()}
				}
				progress(wire.Moving{Step: wire.MoveFailed, Error: we})
			} else {
				progress(wire.Moving{Step: wire.MoveDone, Agent: res.ID})
			}
		}
		return res, err
	}
	peers := s.peerSet()
	if peers == nil {
		return ResumeResult{}, wire.Errorf(wire.CodeUnavailable, "machine %s is not connected", target)
	}
	return s.resumeOn(target, peers, rec, fork, tree)
}

// resumeHere starts the session on this Mac: in its folder when this is
// its home, else brought here first (and moved, unless forked).
// progress (optional) gets the steps of bringing it (agents.moving's).
func (s *Service) resumeHere(ctx context.Context, rec *Record, fork bool, tree Tree, progress func(wire.Moving)) (ResumeResult, error) {
	if progress == nil {
		progress = func(wire.Moving) {}
	}
	reg := s.registry()
	if reg == nil {
		return ResumeResult{}, wire.Errorf(wire.CodeUnavailable, "no agents")
	}
	if !fork {
		if l := s.liveNow(rec); l != nil {
			return ResumeResult{}, liveError(rec.SID, l)
		}
	}
	spawn := agents.SessionSpawn{Kind: rec.Kind, SessionID: rec.SID, Name: rec.Meta.Title, Task: rec.Meta.FirstPrompt, Branch: rec.Meta.Branch, Fork: fork,
		Parent: tree.Parent, Depth: tree.Depth}
	if rec.Node == s.db.node {
		dir, err := s.homeDir(ctx, rec)
		if err != nil {
			return ResumeResult{}, err
		}
		spawn.Dir = dir
		a, err := reg.SpawnSession(spawn)
		return ResumeResult{Agent: a}, err
	}
	dir, note, err := s.bring(ctx, rec, fork, progress)
	if err != nil {
		return ResumeResult{}, err
	}
	progress(wire.Moving{Step: wire.MoveResume})
	spawn.Dir = dir
	a, err := reg.SpawnSession(spawn)
	if err != nil {
		return ResumeResult{}, err
	}
	if !fork {
		s.own(rec.Kind, rec.SID)
		s.edit(rec.Key(), func(r *Record, st Stamp) bool {
			r.MovedTo, r.MovedS = s.db.node, st
			return true
		})
	}
	s.scan.poke()
	if note != "" {
		a.Summary = note
	}
	return ResumeResult{Agent: a, Note: note}, nil
}

// homeDir is the session's folder on its home, its worktree recreated
// from its branch when it is gone.
func (s *Service) homeDir(ctx context.Context, rec *Record) (string, error) {
	cwd := rec.Meta.Cwd
	if cwd == "" {
		return "", wire.Errorf(wire.CodeNotFound, "the session has no folder")
	}
	if isDir(cwd) {
		return cwd, nil
	}
	repo := ""
	if p := s.projectReg(); p != nil && rec.Meta.ProjectID != "" {
		repo = p.PathOn(rec.Meta.ProjectID)
	}
	if repo == "" || rec.Meta.Branch == "" {
		return "", wire.Errorf(wire.CodeNotFound, "%s is gone and there is no branch to recreate it from", cwd)
	}
	if err := s.addWorktree(ctx, repo, cwd, rec.Meta.Branch); err != nil {
		return "", err
	}
	return cwd, nil
}

// addWorktree checks branch out at dir (from the local branch, else the
// remote's).
func (s *Service) addWorktree(ctx context.Context, repo, dir, branch string) error {
	env := s.gitEnv()
	gitRun(ctx, repo, env, "worktree", "prune")
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	if _, err := gitRun(ctx, repo, env, "rev-parse", "--verify", "-q", "refs/heads/"+branch); err == nil {
		if _, err := gitRun(ctx, repo, env, "worktree", "add", dir, branch); err == nil {
			return nil
		}
	}
	gitRun(ctx, repo, env, "fetch", "-q", "origin", branch)
	if _, err := gitRun(ctx, repo, env, "rev-parse", "--verify", "-q", "refs/remotes/origin/"+branch); err == nil {
		if _, err := gitRun(ctx, repo, env, "worktree", "add", "-B", branch, dir, "origin/"+branch); err == nil {
			return nil
		}
	}
	return wire.Errorf(wire.CodeNotFound, "branch %s is neither on this Mac nor on its remote", branch)
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// pseudoAgent describes a session as the agent handoff packs.
func (s *Service) pseudoAgent(rec *Record) wire.Agent {
	var b [4]byte
	rand.Read(b[:])
	created := time.UnixMilli(rec.Meta.StartedAt).UTC()
	return wire.Agent{ID: s.machine() + "/s" + hex.EncodeToString(b[:]), Kind: rec.Kind, Name: rec.Meta.Title, Task: rec.Meta.FirstPrompt,
		Project: rec.Meta.Cwd, ProjectID: rec.Meta.ProjectID, Branch: rec.Meta.Branch, SessionID: rec.SID, Created: created}
}

// ExportPrefix marks agents.export ids that name a session (its key).
const ExportPrefix = "session:"

// ExportForkMark follows ExportPrefix for a fork's bundle: a copy, so
// the session may still be running (in an agent or another program).
const ExportForkMark = "fork:"

// exportKey splits an agents.export session id into its key and
// whether it is for a fork.
func exportKey(id string) (string, bool) {
	key := strings.TrimPrefix(id, ExportPrefix)
	if k, ok := strings.CutPrefix(key, ExportForkMark); ok {
		return k, true
	}
	return key, false
}

// Pack writes a session's move bundle (its transcript and its folder's
// code with the uncommitted work) for agents.export on its home.
func (s *Service) Pack(ctx context.Context, id string, have []string, dir string) (err error) {
	key, fork := exportKey(id)
	rw, err := s.db.get(key)
	if err != nil {
		return err
	}
	if rw == nil || rw.rec.Deleted || rw.rec.Node != s.db.node {
		return wire.Errorf(wire.CodeNotFound, "no session %s here", key)
	}
	rec := &rw.rec
	// A move needs the session at rest; a fork copies it as it is.
	if l := s.liveNow(rec); l != nil && !fork {
		return liveError(rec.SID, l)
	}
	reg := s.registry()
	if reg == nil {
		return wire.Errorf(wire.CodeUnavailable, "no agents")
	}
	if _, err := s.homeDir(ctx, rec); err != nil {
		return err
	}
	m, err := handoff.Pack(ctx, s.pseudoAgent(rec), s.machine(), have, dir, reg.HandoffPaths())
	if err != nil || m.Agent.Transcript != "" {
		return err
	}
	// A transcript handoff does not look for (Codex's .zst, its
	// archive): from where the index found it.
	if err := copyTranscript(rec.Meta.Path, filepath.Join(dir, handoff.TranscriptFile)); err != nil {
		return wire.Errorf(wire.CodeNotFound, "the transcript of %s is gone", rec.SID)
	}
	m.Agent.SessionID, m.Agent.Transcript = rec.SID, transcriptName(rec)
	if size, limit := dirBytes(dir), reg.TransferCap(); size > limit {
		return wire.Errorf(wire.CodeTooLarge, "the session's code and conversation are %d MB, over the %d MB a transfer carries (settings.json maxTransferMB)", size>>20, limit>>20)
	}
	return handoff.WriteManifest(dir, m)
}

// copyTranscript copies a transcript, decompressing a .zst one.
func copyTranscript(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	var r io.Reader = in
	if strings.HasSuffix(from, ".zst") {
		zr, err := newZstdReader(in)
		if err != nil {
			return err
		}
		defer zr.Close()
		r = zr
	}
	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Exportable checks an agents.export id that names a session.
func (s *Service) Exportable(id string) error {
	key, _ := exportKey(id)
	rw, err := s.db.get(key)
	if err != nil {
		return err
	}
	if rw == nil || rw.rec.Deleted || rw.rec.Node != s.db.node {
		return wire.Errorf(wire.CodeNotFound, "no session here")
	}
	return nil
}

// bring places another Mac's session here: from its home when that is
// reachable (transcript, branch and uncommitted work, as agents.move
// does), else from this Mac's mirror on its branch as pushed. It
// returns the folder to resume in and a note about what did not come.
func (s *Service) bring(ctx context.Context, rec *Record, fork bool, progress func(wire.Moving)) (string, string, error) {
	home := s.nameOf(rec.Node, rec.Home)
	stage, err := os.MkdirTemp(s.opt.StateDir, "session-")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(stage)
	os.Chmod(stage, 0o700)
	var homeErr error
	why := home + " is not reachable"
	peers := s.peerSet()
	if peers != nil && linked(peers, home) {
		bundle := filepath.Join(stage, "bundle")
		err := s.fetchFromHome(ctx, peers, home, rec, bundle, fork, progress)
		if err == nil {
			progress(wire.Moving{Step: wire.MoveWorktree})
			_, placed, err := s.registry().PlaceSession(ctx, bundle)
			if err != nil {
				return "", "", err
			}
			dir := placed.Project
			if placed.Worktree != "" {
				dir = placed.Worktree
			}
			if !placed.Resume {
				return "", "", wire.Errorf(wire.CodeNotFound, "%s has no transcript of %s", home, rec.SID)
			}
			return dir, "", nil
		}
		// Only a home that went away (or a transfer that stopped moving)
		// falls back to the mirror: anything else (live, conflicts in
		// the folder here, too large, …) is the answer.
		if !awayError(err) || ctx.Err() != nil {
			return "", "", err
		}
		homeErr = err
		why = "the transfer from " + home + " failed (" + errText(err) + ")"
		s.opt.Logf("history: %s from %s: %v (trying this Mac's copy)", rec.SID, home, err)
	} else {
		homeErr = wire.Errorf(wire.CodeUnavailable, "%s is not connected", home)
	}
	progress(wire.Moving{Step: wire.MoveWorktree})
	dir, note, err := s.fromMirror(ctx, rec, stage, home, why)
	if err != nil {
		// Both ways failed: say why, for both.
		code := wire.CodeUnavailable
		var we *wire.Error
		if errors.As(err, &we) && we.Code != wire.CodeUnavailable {
			code = we.Code
		}
		out := wire.Errorf(code, "could not get the session from %s: %s; this Mac's copy: %s", home, errText(homeErr), errText(err))
		s.opt.Logf("history: %s: %v", rec.SID, out)
		return "", "", out
	}
	return dir, note, nil
}

// awayError: the home could not be reached, or the transfer from it
// stopped (timed out, connection lost).
func awayError(err error) bool {
	var we *wire.Error
	if errors.As(err, &we) {
		return we.Code == wire.CodeUnavailable || we.Code == wire.CodeOffline
	}
	return errors.Is(err, context.DeadlineExceeded)
}

func errText(err error) string {
	var we *wire.Error
	if errors.As(err, &we) {
		return we.Message
	}
	return err.Error()
}

// fromMirror resumes the session from this Mac's copy of its transcript,
// on its branch as pushed.
func (s *Service) fromMirror(ctx context.Context, rec *Record, stage, home, why string) (string, string, error) {
	paths := s.registry().HandoffPaths()
	transcript := filepath.Join(stage, handoff.TranscriptFile)
	ok, err := s.mirrored(rec.Key(), transcript)
	if err != nil {
		return "", "", err
	}
	if !ok {
		return "", "", wire.Errorf(wire.CodeUnavailable, "there is none (this Mac copies the transcripts of sessions active in the last %d days while their Mac is reachable; this one is not copied yet)", s.opt.MirrorDays)
	}
	dir, note, err := s.offlineDir(ctx, rec)
	if err != nil {
		return "", "", err
	}
	m := handoff.Manifest{Version: handoff.Version, ID: "ho-" + randHex(6), Created: time.Now().Unix()}
	m.Source.Machine, m.Source.Home = home, rec.Meta.UserHome
	m.Agent = handoff.AgentInfo{LocalID: "s" + randHex(4), Kind: rec.Kind, Name: rec.Meta.Title, Task: rec.Meta.FirstPrompt,
		Created: time.UnixMilli(rec.Meta.StartedAt).UTC(), SessionID: rec.SID, Transcript: transcriptName(rec)}
	m.Project = handoff.ProjectInfo{Path: dir}
	if err := handoff.WriteManifest(stage, &m); err != nil {
		return "", "", err
	}
	if _, _, err := handoff.Unpack(ctx, stage, paths); err != nil {
		return "", "", moveError(err)
	}
	msg := why + ": resumed from this Mac's copy of the conversation"
	if rec.Meta.Branch != "" {
		msg += " on " + rec.Meta.Branch + " as pushed; uncommitted work there did not come along"
	}
	if note != "" {
		msg += "; " + note
	}
	return dir, msg, nil
}

func (s *Service) fetchFromHome(ctx context.Context, peers Peers, home string, rec *Record, dir string, fork bool, progress func(wire.Moving)) error {
	progress(wire.Moving{Step: wire.MoveCheckpoint})
	params, _ := json.Marshal(hostParams{Key: rec.Key()})
	pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	raw, err := peers.Call(pctx, home, "sessions.plan", params)
	cancel()
	if err != nil {
		return err
	}
	var plan handoff.Plan
	if err := json.Unmarshal(raw, &plan); err != nil {
		return err
	}
	// The commits this Mac has: incremental from them. Without the
	// repository it clones it from its remote (PlaceSession), which has
	// the remote's head: the bundle carries only what is newer.
	var have []string
	if plan.Git || len(plan.Commits) > 0 {
		probe := s.registry().ProbeMove(ctx, plan.Project, plan.Home, plan.Commits, plan.ProjectID, "")
		for c, ok := range probe.Has {
			if ok {
				have = append(have, c)
			}
		}
		if !probe.Exists && plan.Remote != "" && plan.RemoteHead != "" {
			have = []string{plan.RemoteHead}
		}
		sortStrings(have)
	}
	id := ExportPrefix + rec.Key()
	if fork {
		id = ExportPrefix + ExportForkMark + rec.Key()
	}
	return peers.Fetch(ctx, home, id, have, dir, func(got, total int64) {
		m := wire.Moving{Step: wire.MoveTransfer, Bytes: got, Total: total}
		if total > 0 {
			m.Percent = int(float64(got) / float64(total) * 100)
		}
		progress(m)
	})
}

// offlineDir is where the session resumes here without its home: its
// folder (mapped to this home) when it exists, else a worktree of its
// project on its branch.
func (s *Service) offlineDir(ctx context.Context, rec *Record) (string, string, error) {
	paths := s.registry().HandoffPaths()
	cwd := handoff.MapPath(rec.Meta.Cwd, rec.Meta.UserHome, paths.Home)
	if cwd != "" && isDir(cwd) {
		return cwd, "", nil
	}
	repo := ""
	if p := s.projectReg(); p != nil && rec.Meta.ProjectID != "" {
		repo = p.PathOn(rec.Meta.ProjectID)
	}
	if repo == "" {
		return "", "", wire.Errorf(wire.CodeNotFound, "the session's folder %s is not on this Mac, nor is its project (bring the project here, or resume when %s is reachable: then it is cloned from its remote)",
			cwd, s.nameOf(rec.Node, rec.Home))
	}
	if rec.Meta.Branch == "" || cwd == "" || filepath.Base(cwd) == filepath.Base(repo) {
		return repo, "", nil
	}
	if err := s.addWorktree(ctx, repo, cwd, rec.Meta.Branch); err != nil {
		return "", "", err
	}
	return cwd, "", nil
}

// transcriptName is the transcript's name in a bundle (handoff): Claude's
// file name, Codex's path below sessions/ (its date folders).
func transcriptName(rec *Record) string {
	if rec.Kind == wire.KindClaude {
		return rec.SID + ".jsonl"
	}
	base := strings.TrimSuffix(filepath.Base(rec.Meta.Path), ".zst")
	if i := strings.Index(rec.Meta.Path, "/sessions/"); i >= 0 {
		rel := strings.TrimSuffix(rec.Meta.Path[i+len("/sessions/"):], ".zst")
		if strings.Count(rel, "/") == 3 {
			return rel
		}
	}
	// archived_sessions/rollout-YYYY-MM-DDT…: back under its date.
	if strings.HasPrefix(base, "rollout-") && len(base) > 18 {
		d := base[8:18]
		return d[0:4] + "/" + d[5:7] + "/" + d[8:10] + "/" + base
	}
	t := time.UnixMilli(rec.Meta.StartedAt).UTC()
	return t.Format("2006/01/02") + "/rollout-" + t.Format("2006-01-02T15-04-05") + "-" + rec.SID + ".jsonl"
}

func moveError(err error) error {
	if code := handoff.CodeOf(err); code != "" {
		switch code {
		case "manifest", "invalid":
			return wire.Errorf(wire.CodeInvalid, "%v", err)
		case "missing_project":
			return wire.Errorf(wire.CodeNotFound, "%v", err)
		default:
			return wire.Errorf(wire.CodeExists, "%v", err)
		}
	}
	return err
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func newZstdReader(r io.Reader) (io.ReadCloser, error) {
	d, err := zstd.NewReader(r, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true))
	if err != nil {
		return nil, err
	}
	return d.IOReadCloser(), nil
}

// --- brief, continue ---

// brief is a hand-over text built from the transcript's index entry (no
// model call): the task, what was asked along the way, the last state,
// the todos and the changed files.
func (s *Service) brief(rec *Record) string {
	m := &rec.Meta
	var b strings.Builder
	kind := map[string]string{wire.KindClaude: "Claude Code", wire.KindCodex: "Codex"}[rec.Kind]
	fmt.Fprintf(&b, "You are continuing a %s session (%s) from %s.\n", kind, rec.SID, s.nameOf(rec.Node, rec.Home))
	if m.Title != "" {
		fmt.Fprintf(&b, "Title: %s\n", m.Title)
	}
	if m.Cwd != "" {
		fmt.Fprintf(&b, "Folder: %s", m.Cwd)
		if m.Branch != "" {
			fmt.Fprintf(&b, " (branch %s)", m.Branch)
		}
		b.WriteString("\n")
	}
	if m.FirstPrompt != "" {
		fmt.Fprintf(&b, "\n## Task\n%s\n", m.FirstPrompt)
	}
	prompts := splitPieces(m.Prompts)
	if len(prompts) > 2 {
		b.WriteString("\n## Asked along the way (decisions and changes of direction)\n")
		mid := prompts[1 : len(prompts)-1]
		if len(mid) > 12 {
			mid = mid[len(mid)-12:]
		}
		for _, p := range mid {
			fmt.Fprintf(&b, "- %s\n", clip(oneLine(p), 300))
		}
	}
	if m.LastUser != "" && m.LastUser != m.FirstPrompt {
		fmt.Fprintf(&b, "\n## Last request\n%s\n", m.LastUser)
	}
	if m.LastAssistant != "" {
		fmt.Fprintf(&b, "\n## Last state (the agent's last answer)\n%s\n", m.LastAssistant)
	}
	if len(m.Todos) > 0 {
		b.WriteString("\n## Todos\n")
		for _, t := range m.Todos {
			mark := " "
			if t.Done {
				mark = "x"
			}
			fmt.Fprintf(&b, "- [%s] %s\n", mark, t.Text)
		}
	}
	if rec.Node == s.db.node && m.Cwd != "" && isDir(m.Cwd) {
		if ch := s.gitc.get(m.Cwd, s.gitEnv()); ch != nil && len(ch.Files) > 0 {
			b.WriteString("\n## Changed files")
			if ch.Uncommitted {
				b.WriteString(" (some uncommitted)")
			}
			b.WriteString("\n")
			for i, f := range ch.Files {
				if i == 40 {
					fmt.Fprintf(&b, "- … %d more\n", len(ch.Files)-40)
					break
				}
				fmt.Fprintf(&b, "- %s +%d −%d\n", f.Path, f.Added, f.Removed)
			}
		}
	}
	if m.Path != "" {
		fmt.Fprintf(&b, "\nThe full transcript is %s on %s.\n", m.Path, s.nameOf(rec.Node, rec.Home))
	}
	b.WriteString("\nPick up where it left off: check the folder's state first, then go on with what is open.")
	return b.String()
}

// ContinueAs runs sessions.continueAs: the other kind (or the same) starts
// in the session's folder with the brief as its first prompt.
func (s *Service) ContinueAs(id, kind, machine string) (wire.Agent, error) {
	return s.continueAs(id, kind, machine, Tree{})
}

// continueAs is ContinueAs with the new agent's place in the agent tree.
func (s *Service) continueAs(id, kind, machine string, tree Tree) (wire.Agent, error) {
	if kind != wire.KindClaude && kind != wire.KindCodex {
		return wire.Agent{}, wire.Errorf(wire.CodeInvalid, "kind must be claude or codex")
	}
	rw, err := s.find(id)
	if err != nil {
		return wire.Agent{}, err
	}
	rw = s.follow(rw)
	rec := &rw.rec
	target := machine
	if target == "" {
		target = s.nameOf(rec.Node, rec.Home)
	}
	if target == s.machine() {
		return s.continueHere(rec, kind, tree)
	}
	peers := s.peerSet()
	if peers == nil {
		return wire.Agent{}, wire.Errorf(wire.CodeUnavailable, "machine %s is not connected", target)
	}
	params, _ := json.Marshal(hostParams{Key: rec.Key(), Kind: kind, treeParams: tree.params()})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	raw, err := peers.Call(ctx, target, "sessions.continueAs", params)
	if err != nil {
		return wire.Agent{}, err
	}
	var a wire.Agent
	if err := json.Unmarshal(raw, &a); err != nil {
		return wire.Agent{}, wire.Errorf(wire.CodeRemote, "bad answer from %s", target)
	}
	if _, local, ok := strings.Cut(a.ID, "/"); ok {
		a.ID, a.Machine = target+"/"+local, target
	}
	return a, nil
}

func (s *Service) continueHere(rec *Record, kind string, tree Tree) (wire.Agent, error) {
	reg := s.registry()
	if reg == nil {
		return wire.Agent{}, wire.Errorf(wire.CodeUnavailable, "no agents")
	}
	if kind != wire.KindClaude && kind != wire.KindCodex {
		return wire.Agent{}, wire.Errorf(wire.CodeInvalid, "kind must be claude or codex")
	}
	dir := rec.Meta.Cwd
	if rec.Node != s.db.node {
		dir = handoff.MapPath(dir, rec.Meta.UserHome, reg.HandoffPaths().Home)
	}
	if !isDir(dir) {
		dir = ""
		if p := s.projectReg(); p != nil && rec.Meta.ProjectID != "" {
			dir = p.PathOn(rec.Meta.ProjectID)
		}
	}
	if dir == "" {
		return wire.Agent{}, wire.Errorf(wire.CodeNotFound, "the session's folder is not on this Mac")
	}
	name := "continue " + rec.Meta.Title
	return reg.Spawn(wire.SpawnParams{Kind: kind, Project: dir, Task: s.brief(rec), Name: clip(oneLine(name), 60),
		Parent: tree.Parent, Depth: tree.Depth})
}

// --- archive, delete ---

// Archive runs sessions.archive.
func (s *Service) Archive(id string, archived bool) (wire.Session, error) {
	rw, err := s.find(id)
	if err != nil {
		return wire.Session{}, err
	}
	r, err := s.edit(rw.rec.Key(), func(r *Record, st Stamp) bool {
		if r.Archived == archived {
			return false
		}
		r.Archived, r.ArchivedS = archived, st
		return true
	})
	if err != nil {
		return wire.Session{}, err
	}
	return s.toWire(r), nil
}

// Delete runs sessions.delete: a tombstone now (it reaches every Mac);
// the home removes the transcript 30 s later unless undone.
func (s *Service) Delete(id string, undo bool) error {
	rw, err := s.find(id)
	if err != nil {
		return err
	}
	rec := &rw.rec
	if undo {
		if !rec.Deleted {
			return nil
		}
		if rec.Node == s.db.node && rec.Meta.Path != "" && !fileExists(rec.Meta.Path) {
			return wire.Errorf(wire.CodeInvalid, "too late: the transcript is gone")
		}
		_, err := s.edit(rec.Key(), func(r *Record, st Stamp) bool {
			r.Deleted, r.DeletedS = false, st
			return true
		})
		return err
	}
	if l := s.liveNow(rec); l != nil {
		return liveError(rec.SID, l)
	}
	_, err = s.edit(rec.Key(), func(r *Record, st Stamp) bool {
		if r.Deleted {
			return false
		}
		r.Deleted, r.DeletedS = true, st
		return true
	})
	if err == nil {
		s.deletes.kick()
	}
	return err
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// deleter removes the transcripts of this Mac's deleted sessions once
// their undo window passed (Claude: the file; Codex: moved to
// ~/.codex/archived_sessions, Codex's own archive).
type deleter struct {
	s    *Service
	wake chan struct{}
}

func newDeleter(s *Service) *deleter {
	d := &deleter{s: s, wake: make(chan struct{}, 1)}
	s.wg.Add(1)
	go func() { defer s.wg.Done(); d.run() }()
	return d
}

func (d *deleter) kick() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

func (d *deleter) run() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-d.s.stop:
			return
		case <-t.C:
		case <-d.wake:
		}
		d.sweep()
	}
}

func (d *deleter) sweep() {
	s := d.s
	rows, err := s.db.r.Query(`SELECT kind, sid, path, deleted_s FROM sessions WHERE node = ? AND deleted = 1 AND path != ''`, s.db.node)
	if err != nil {
		return
	}
	type item struct{ kind, sid, path, stamp string }
	var items []item
	for rows.Next() {
		var it item
		if rows.Scan(&it.kind, &it.sid, &it.path, &it.stamp) == nil {
			items = append(items, it)
		}
	}
	rows.Close()
	now := s.opt.Now()
	archive := filepath.Join(s.opt.CodexHome, "archived_sessions")
	for _, it := range items {
		st := parseStamp(it.stamp)
		if now.Sub(time.UnixMilli(st.T)) < UndoWindow {
			continue
		}
		if !fileExists(it.path) || strings.HasPrefix(it.path, archive+"/") {
			continue
		}
		var err error
		if it.kind == wire.KindCodex {
			if err = os.MkdirAll(archive, 0o700); err == nil {
				err = os.Rename(it.path, filepath.Join(archive, filepath.Base(it.path)))
			}
		} else {
			err = os.Remove(it.path)
		}
		if err != nil {
			s.opt.Logf("history: removing %s: %v", it.path, err)
			continue
		}
		s.opt.Logf("history: removed the transcript of deleted session %s", it.sid)
	}
	if len(items) > 0 {
		s.scan.poke()
	}
}
