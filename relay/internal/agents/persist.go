package agents

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// agents.json: the registry, written atomically (temp file, fsync,
// rename) shortly after every change.

type record struct {
	wire.Agent
	Local string `json:"local"`
	// Running: the process ran when the daemon last saved; it is
	// respawned (resumed) when the daemon starts.
	Running     bool   `json:"running"`
	PendingTask string `json:"pendingTask,omitempty"`
	EndedTurn   string `json:"endedTurn,omitempty"`
	// SessionSeen: a hook showed the session was saved.
	SessionSeen bool `json:"sessionSeen,omitempty"`
	// Engaged: the agent got past starting (it took its task); a fresh
	// start then never sends the task again.
	Engaged bool `json:"engaged,omitempty"`
	// Respawn: a daemon stop ended it and its respawn failed (shown as
	// error "Not resumed"): the next daemon start tries again.
	Respawn bool `json:"respawn,omitempty"`
	// Closing (closing agents): it was being closed (the reason) when the
	// daemon stopped: it is not restored.
	Closing string `json:"closing,omitempty"`
}

// engagedRecord: a saved agent got past starting. Files written before
// the flag existed say so by their state, summary or session.
func engagedRecord(rec record) bool {
	if rec.Engaged || rec.SessionSeen || rec.Summary != "" {
		return true
	}
	switch rec.State {
	case wire.StateWorking, wire.StateApproval, wire.StateDone:
		return true
	case wire.StateIdle:
		return rec.PendingTask == ""
	}
	return false
}

type stateFile struct {
	Version int      `json:"version"`
	Agents  []record `json:"agents"`
}

func (r *Registry) agentsPath() string { return filepath.Join(r.opt.StateDir, "agents.json") }

func (r *Registry) loadAgents() ([]record, error) {
	data, err := os.ReadFile(r.agentsPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f stateFile
	if err := json.Unmarshal(data, &f); err != nil {
		// Keep the broken file for a look; start empty.
		os.Rename(r.agentsPath(), r.agentsPath()+".broken")
		r.opt.Logf("hesperd: agents.json: %v (moved to agents.json.broken)", err)
		return nil, nil
	}
	return f.Agents, nil
}

// restore puts a saved agent back: respawned with its session when it was
// running, else as it ended.
func (r *Registry) restore(rec record) {
	projectID := r.projectOf(rec.Agent.Dir()) // projects step 1
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec.Local == "" || r.agents[rec.Local] != nil {
		return
	}
	if rec.Closing != "" {
		r.opt.Logf("hesperd: %s was being closed (%s): not restored", rec.ID, rec.Closing)
		r.scheduleSave()
		return
	}
	a := &agent{Agent: rec.Agent, local: rec.Local, endedTurn: rec.EndedTurn, sessionSeen: rec.SessionSeen, engaged: engagedRecord(rec)}
	if projectID != "" {
		a.ProjectID = projectID
	}
	// The machine's short name may have changed since.
	a.ID, a.Machine = r.id(rec.Local), r.machine
	r.agents[a.local] = a
	if !rec.Running && !rec.Respawn {
		a.running = false
		if a.State != wire.StateExited && a.State != wire.StateError {
			a.State, a.Attention = wire.StateExited, nil
		}
		return
	}
	if a.Kind == wire.KindCodex && a.SessionID == "" {
		a.started = rec.Created
		r.adoptCodexSession(a)
	}
	profile, err := r.profileOf(a)
	fresh := false
	if err == nil {
		fresh, err = r.comeBack(a, profile)
	}
	if err != nil {
		r.opt.Logf("hesperd: %s not respawned: %v", a.ID, err)
		// Shown as an error with the reason; it stays to be respawned
		// (the next daemon start, agents.resume).
		a.Exit = nil
		r.notResumed(a, err)
		a.respawn = true
		if errors.Is(err, exec.ErrNotFound) {
			// Its command is gone for the moment (an update reinstalling
			// it): try again once it is back.
			go r.respawnLater(a.local, profile)
		}
	}
	if rec.PendingTask != "" && !fresh && a.term != nil && a.Exit == nil && a.pendingTask == "" {
		// The task never reached the agent: deliver it now.
		a.pendingTask = rec.PendingTask
		go r.deliverLater(a.local, a.gen)
	}
	r.scheduleSave()
}

// respawnLater respawns a restored agent whose command was missing at the
// daemon's start, once the command is back (within CommandWait).
func (r *Registry) respawnLater(local string, profile wire.Profile) {
	if r.waitCommand(profile) != nil {
		return // still an error: agents.resume or the next start
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.agents[local]
	if a == nil || r.closing || !a.respawn || (a.term != nil && a.Exit == nil) {
		return
	}
	if _, err := r.comeBack(a, profile); err != nil {
		a.Exit = nil
		r.notResumed(a, err)
		a.respawn = true
		return
	}
	r.opt.Logf("hesperd: %s respawned (its command is back)", a.ID)
	r.changed(a)
}

// scheduleSave asks the saver to write soon (the lock is held).
func (r *Registry) scheduleSave() {
	if r.saveCh == nil || r.closing {
		return
	}
	select {
	case r.saveCh <- struct{}{}:
	default:
	}
}

func (r *Registry) saver() {
	defer close(r.saverEnd)
	for range r.saveCh {
		time.Sleep(50 * time.Millisecond) // coalesce bursts of hooks
		if err := r.writeState(); err != nil {
			r.opt.Logf("hesperd: saving agents.json: %v", err)
		}
	}
}

func (r *Registry) writeState() error {
	r.mu.Lock()
	f := stateFile{Version: 1}
	for _, a := range r.agents {
		f.Agents = append(f.Agents, record{Agent: a.Agent, Local: a.local, Running: a.running, PendingTask: a.pendingTask, EndedTurn: a.endedTurn, SessionSeen: a.sessionSeen, Engaged: a.engaged, Respawn: a.respawn, Closing: a.closeReason})
	}
	r.mu.Unlock()
	sort.Slice(f.Agents, func(i, j int) bool { return f.Agents[i].Created.Before(f.Agents[j].Created) })
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(r.agentsPath(), append(data, '\n'), 0o600)
}

// atomicWrite replaces path with data: a temp file in the same directory,
// synced, renamed over it.
func atomicWrite(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Recent projects: projects.json, the projects agents were started in.

const recentLimit = 30

func (r *Registry) projectsPath() string { return filepath.Join(r.opt.StateDir, "projects.json") }

func (r *Registry) loadProjects() {
	if r.opt.Projects != nil {
		return // projects step 1: the project store owns projects.json
	}
	data, err := os.ReadFile(r.projectsPath())
	if err != nil {
		return
	}
	var list []wire.Project
	if json.Unmarshal(data, &list) != nil {
		return
	}
	for _, p := range list {
		r.projects[p.Path] = p
	}
}

// touchProject records a project's use (the lock is held).
func (r *Registry) touchProject(path string, at time.Time) {
	if r.opt.Projects != nil { // projects step 1
		r.opt.Projects.Touch(path, at)
		return
	}
	r.projects[path] = wire.Project{Path: path, Name: filepath.Base(path), LastUsed: at}
	list := r.recentLocked()
	if len(list) > recentLimit {
		for _, p := range list[recentLimit:] {
			delete(r.projects, p.Path)
		}
		list = list[:recentLimit]
	}
	data, _ := json.MarshalIndent(list, "", "  ")
	if err := atomicWrite(r.projectsPath(), append(data, '\n'), 0o600); err != nil {
		r.opt.Logf("hesperd: saving projects.json: %v", err)
	}
}

// Recent is the recent projects, most recent first.
func (r *Registry) Recent() []wire.Project {
	if r.opt.Projects != nil { // projects step 1: projects first
		return r.opt.Projects.Recent()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.recentLocked()
}

func (r *Registry) recentLocked() []wire.Project {
	list := make([]wire.Project, 0, len(r.projects))
	for _, p := range r.projects {
		list = append(list, p)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].LastUsed.After(list[j].LastUsed) })
	return list
}
