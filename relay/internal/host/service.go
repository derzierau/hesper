// Package host is hesperd's host role (rebuild contract part R): it serves
// this machine's agents (internal/agents) to approved controllers through
// the relay or the direct path. Every request is authorized by part K
// (device keys, rights), travels inside part N's end-to-end channel, and
// runs one at a time (Runner.perform).
//
// What the relay sees of this machine is the published snapshot only:
// agent ids, kinds, states, attention kinds, sizes, capabilities and
// machine stats. Names, tasks, attention details and terminal bytes go
// only to controllers, sealed: as request results, on a link's events and
// on its attach channels (link.go).
package host

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/internal/agents"
	"github.com/derzierau/hesper/relay/internal/projects"
	"github.com/derzierau/hesper/relay/pkg/devicekey"
	"github.com/derzierau/hesper/relay/pkg/protocol"
	"github.com/derzierau/hesper/relay/pkg/session"
	"github.com/derzierau/hesper/relay/pkg/transfer"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

type Service struct {
	// Agents is this machine's registry (hesperd's).
	Agents *agents.Registry
	// Handoff (optional) moves agents in and out (transfer, agents.import,
	// agents.export, download, job); Machine (optional) adds machine stats
	// to snapshots.
	Handoff *Handoff
	Machine *MachineMonitor
	// AllowShell (hesperd serve --allow-shell) lets devices with the shell
	// right start shells here (agents.spawn kind shell, Touch ID) and see,
	// attach to and type into shell agents. Without it shells stay local.
	AllowShell bool
	// E2EKey (base64) advertises the end-to-end channel in snapshots
	// (Runner.E2E answers it).
	E2EKey string
	// Direct (optional) advertises the direct path while it listens.
	Direct *Direct
	// Audit (optional) writes an audit line (Authorizer.Audit): one per
	// attachment upload (files.go).
	Audit func(event string, fields map[string]any)
	// Projects (optional; projects step 1) is the shared project
	// registry: projects.sync / projects.promote, and its state on links
	// (projects.go).
	Projects *projects.Store
	// Sessions (optional; shared history): the sessions.* host methods
	// and change hints on links (sessions.go).
	Sessions SessionsHost

	links       links
	reviewParts partCache // review.go
	runtime     string
	runtimeOnce sync.Once
}

func params(raw json.RawMessage, result any) error {
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(result); err != nil {
		return protocol.Err("invalid_request", "Invalid method parameters")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return protocol.Err("invalid_request", "Expected one parameter object")
	}
	return nil
}

// runtimeID names this daemon run (a restart makes old ids stale).
func (s *Service) runtimeID() string {
	s.runtimeOnce.Do(func() {
		var b [8]byte
		rand.Read(b[:])
		s.runtime = hex.EncodeToString(b[:])
	})
	return s.runtime
}

// Snapshot is what this machine publishes through the relay: no names, no
// tasks, no attention texts. Every agent is a terminal whose terminalId
// is its local id, role its kind, state and attention its state and
// attention kind (push notifications read those), plus its size.
func (s *Service) Snapshot(ctx context.Context) (session.Snapshot, error) {
	state := session.Snapshot{RuntimeID: s.runtimeID(), Terminals: []session.Terminal{}}
	state.Capabilities.Agents = s.Agents != nil
	state.Capabilities.Ping = true
	state.Capabilities.Shell = s.AllowShell
	if s.Agents != nil {
		state.Short = s.Agents.Machine()
		for _, a := range s.Agents.List() {
			if a.Kind == wire.KindShell && !s.AllowShell {
				continue
			}
			t := session.Terminal{Target: session.Target{RuntimeID: state.RuntimeID, TerminalID: localID(a.ID)},
				Role: a.Kind, State: a.State, Columns: a.Size.Cols, Rows: a.Size.Rows, Exited: a.Exit != nil}
			if a.Attention != nil {
				t.Attention, t.AttentionSince = a.Attention.Kind, a.StateSince.Unix()
			}
			state.Terminals = append(state.Terminals, t)
		}
	}
	if s.Handoff != nil && s.Handoff.Key != nil {
		state.Capabilities.Transfer = s.Handoff.started() == nil
		state.Capabilities.Spawn = s.Handoff.Available()
		if state.Capabilities.Transfer {
			state.TransferKey = transfer.EncodePublicKey(s.Handoff.Key.PublicKey())
		}
	}
	if s.E2EKey != "" {
		state.Capabilities.E2E, state.E2EKey = true, s.E2EKey
		state.Capabilities.Direct = s.Direct.Listening()
	}
	if s.Machine != nil {
		state.Machine = s.Machine.Stats(ctx, state.Terminals)
	}
	return state, nil
}

// Execute runs one authorized request (Runner.perform checked part K).
func (s *Service) Execute(ctx context.Context, m protocol.Message) (json.RawMessage, error) {
	if m.Deadline == 0 || time.Now().After(time.UnixMilli(m.Deadline)) {
		return nil, protocol.Err("expired", "Request expired before execution")
	}
	ctx, cancel := context.WithDeadline(ctx, time.UnixMilli(m.Deadline))
	defer cancel()
	switch m.Method {
	case "snapshot":
		state, err := s.Snapshot(ctx)
		return protocol.JSON(state), err
	case "ping":
		// The controller's round trip to this host: nothing to do,
		// nothing to reveal.
		var p struct{}
		if err := params(m.Params, &p); err != nil {
			return nil, err
		}
		return json.RawMessage(`{}`), nil
	case "transfer", "job", "download", "agents.import", "agents.export":
		return s.handoff(ctx, m)
	case "projects.sync", "projects.promote",
		"projects.scratch", "projects.scratchArchive", "projects.scratchRestore", "projects.scratchDelete": // scratch projects
		return s.projects(ctx, m) // projects step 1
	case "sessions.pull", "sessions.transcript", "sessions.plan", "sessions.changes", "sessions.resume", "sessions.fork", "sessions.continueAs",
		"checkpoints.restore": // move work
		return s.sessions(ctx, m) // shared history
	}
	if s.Agents == nil {
		return nil, protocol.Err("unsupported", "Host serves no agents")
	}
	if m.Method == "files.put" || m.Method == "files.chunk" {
		return s.files(ctx, m)
	}
	if strings.HasPrefix(m.Method, "review.") {
		return s.review(ctx, m) // review.go
	}
	return s.agents(ctx, m)
}

// handoff dispatches the methods that carry moves.
func (s *Service) handoff(ctx context.Context, m protocol.Message) (json.RawMessage, error) {
	if err := s.Handoff.started(); err != nil {
		return nil, err
	}
	switch m.Method {
	case "transfer":
		var p transferParams
		if err := params(m.Params, &p); err != nil {
			return nil, err
		}
		return s.Handoff.Transfer(p)
	case "agents.import":
		var p struct {
			Upload string `json:"upload"`
		}
		if err := params(m.Params, &p); err != nil {
			return nil, err
		}
		return s.Handoff.Spawn(p.Upload, 0)
	case "job":
		var p struct {
			Job    string `json:"job"`
			Cancel bool   `json:"cancel"`
		}
		if err := params(m.Params, &p); err != nil {
			return nil, err
		}
		return s.Handoff.Job(p.Job, p.Cancel)
	case "agents.export":
		var p exportParams
		if err := params(m.Params, &p); err != nil {
			return nil, err
		}
		if strings.HasPrefix(p.ID, agents.FolderExportPrefix) {
			// bring the folder: "folder:with:<path>" or
			// "folder:clean:<path>" (the registry checks the path).
			if s.Agents == nil {
				return nil, protocol.Err("unsupported", "Host serves no agents")
			}
			if _, _, err := agents.ParseFolderExport(p.ID); err != nil {
				return nil, publicError(err)
			}
		} else if strings.HasPrefix(p.ID, "session:") && s.Sessions != nil {
			// shared history: a session's bundle (sessions.go)
			if err := s.Sessions.Exportable(p.ID); err != nil {
				return nil, publicError(err)
			}
		} else if p.ID != "" {
			a, err := s.agent(ctx, p.ID)
			if err != nil {
				return nil, err
			}
			p.ID = a.ID
		}
		return s.Handoff.Export(ctx, p)
	}
	var p downloadParams
	if err := params(m.Params, &p); err != nil {
		return nil, err
	}
	return s.Handoff.Download(p)
}

// publicError is a protocol error for the controller: the daemon's codes
// kept (not_found, invalid, exists, unavailable, forbidden), anything else
// "internal".
func publicError(err error) *protocol.Error {
	var we *wire.Error
	if errors.As(err, &we) {
		return protocol.Err(we.Code, we.Message)
	}
	var pe *protocol.Error
	if errors.As(err, &pe) {
		return pe
	}
	return protocol.Err("internal", "Operation failed")
}

func localID(id string) string {
	for i := 0; i < len(id); i++ {
		if id[i] == '/' {
			return id[i+1:]
		}
	}
	return id
}

// visible: whether a caller may see an agent at all (shells need the
// shell right, and --allow-shell).
func (s *Service) visible(ctx context.Context, a wire.Agent) bool {
	if a.Kind != wire.KindShell {
		return true
	}
	caller, ok := CallerFrom(ctx)
	return s.AllowShell && ok && caller.Has(devicekey.Shell)
}

// agent finds one agent for a request (by full or local id); a shell the
// caller may not see does not exist for it.
func (s *Service) agent(ctx context.Context, id string) (wire.Agent, error) {
	if id == "" || len(id) > 64 {
		return wire.Agent{}, protocol.Err("invalid_request", "id is required")
	}
	a, err := s.Agents.Get(s.Agents.Machine() + "/" + localID(id))
	if err != nil {
		return wire.Agent{}, publicError(err)
	}
	if !s.visible(ctx, a) {
		return wire.Agent{}, protocol.Err("not_found", "no agent "+id)
	}
	return a, nil
}
