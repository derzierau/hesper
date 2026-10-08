package host

import (
	"context"
	"encoding/json"
	"time"

	"github.com/derzierau/hesper/relay/internal/projects"
	"github.com/derzierau/hesper/relay/pkg/agentlink"
	"github.com/derzierau/hesper/relay/pkg/protocol"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Projects (workspace model step 1): the project registry is shared
// between the owner's Macs. A host sends its state on every link (when it
// opens and after each change, sealed like the agent events); a
// controller sends its own with projects.sync (signed, through the
// channel) and gets the host's back. Both merge (internal/projects:
// last writer wins per field, tombstones). The relay sees none of it.

// projectsDebounce collects bursts of changes into one link event.
var projectsDebounce = 100 * time.Millisecond

func (s *Service) projects(ctx context.Context, m protocol.Message) (json.RawMessage, error) {
	if s.Projects == nil {
		return nil, protocol.Err("unsupported", "Host keeps no projects")
	}
	switch m.Method {
	case "projects.sync":
		// state: the caller's (right transfer); without it a read
		// (right observe).
		var p struct {
			State *projects.State `json:"state"`
		}
		if err := params(m.Params, &p); err != nil {
			return nil, err
		}
		if p.State != nil {
			s.Projects.Merge(p.State, "")
		}
		return protocol.JSON(s.Projects.Export()), nil
	case "projects.promote":
		var p wire.ProjectPromoteParams
		if err := params(m.Params, &p); err != nil {
			return nil, err
		}
		if p.Machine != "" && s.Agents != nil && p.Machine != s.Agents.Machine() {
			return nil, protocol.Err("invalid", "machine "+p.Machine+" is not this one")
		}
		p.Machine = ""
		res, err := s.Projects.PromoteForPeer(p)
		if err != nil {
			return nil, publicError(err)
		}
		return protocol.JSON(res), nil
	case "projects.scratch", "projects.scratchArchive", "projects.scratchRestore", "projects.scratchDelete":
		// scratch projects: made, archived, restored and deleted on
		// their home (this Mac).
		res, err := s.Projects.ScratchForPeer(m.Method, m.Params)
		if err != nil {
			return nil, publicError(err)
		}
		return protocol.JSON(res), nil
	}
	return nil, protocol.Err("unsupported", "Unsupported operation: "+m.Method)
}

// serveProjects sends the project state on a link: now and after every
// change, until the link ends.
func (s *Service) serveProjects(ctx context.Context, link *hostLink) {
	store := s.Projects
	if store == nil {
		return
	}
	for {
		changed := store.Changes()
		data, err := json.Marshal(store.Export())
		if err == nil && len(data) < agentlink.MaxPayload-64 {
			if link.conn.WriteEvent(ctx, agentlink.Event{Projects: data}) != nil {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-changed:
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(projectsDebounce):
		}
	}
}
