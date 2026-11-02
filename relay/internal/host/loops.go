package host

import (
	"context"
	"encoding/json"
	"time"

	"github.com/derzierau/hesper/relay/internal/loops"
	"github.com/derzierau/hesper/relay/pkg/agentlink"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

// Loops (internal/loops) are shared between the owner's Macs like the
// history is: every link gets a small hint (node, epoch, newest sequence
// number) when it opens and after changes; the controller pulls what it
// lacks (loops.pull, right observe) and pushes its own changes
// (loops.sync with records, right transfer). The hint is also how a
// controller learns this host keeps loops (the "loops" capability: it
// travels inside the end-to-end channel, never in the relay-visible
// snapshot). The relay sees none of it.

// loopsDebounce collects bursts of changes into one hint.
var loopsDebounce = 100 * time.Millisecond

func (s *Service) loops(ctx context.Context, m protocol.Message) (json.RawMessage, error) {
	if s.Loops == nil {
		return nil, protocol.Err("unsupported", "Host keeps no loops")
	}
	switch m.Method {
	case "loops.pull":
		var p loops.PullParams
		if err := params(m.Params, &p); err != nil {
			return nil, err
		}
		return protocol.JSON(s.Loops.Pull(p)), nil
	case "loops.sync":
		var p loops.SyncParams
		if err := params(m.Params, &p); err != nil {
			return nil, err
		}
		return protocol.JSON(s.Loops.Sync(p)), nil
	}
	return nil, protocol.Err("unsupported", "Unsupported operation: "+m.Method)
}

// serveLoops sends the loops hint on a link: now and after every change,
// until the link ends.
func (s *Service) serveLoops(ctx context.Context, link *hostLink) {
	store := s.Loops
	if store == nil {
		return
	}
	for {
		changed := store.Changes()
		if link.conn.WriteEvent(ctx, agentlink.Event{Loops: store.HostHint()}) != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-changed:
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(loopsDebounce):
		}
	}
}

// node is this host's node for its full agent event ("" without
// projects or loops).
func (s *Service) node() string {
	switch {
	case s.Loops != nil:
		return s.Loops.Node()
	case s.Projects != nil:
		return s.Projects.Node()
	}
	return ""
}
