package host

import (
	"context"
	"encoding/json"
	"time"

	"github.com/derzierau/hesper/relay/pkg/agentlink"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

// Shared history (internal/sessions): other Macs pull this Mac's index
// entries (sessions.pull), mirror its recent transcripts
// (sessions.transcript, sealed chunks), plan and export a session for a
// move (sessions.plan, agents.export with a "session:" id), ask for the
// changes in a session's folder (sessions.changes) and start sessions
// here (sessions.resume / sessions.fork / sessions.continueAs). Every
// link gets a small hint (node, newest sequence number) when it opens
// and after changes, so the other side pulls at once. The relay sees
// none of it.

// SessionsHost is the shared history as a host serves it.
type SessionsHost interface {
	HostCall(ctx context.Context, method string, params json.RawMessage) (any, error)
	HostHint() json.RawMessage
	Changes() <-chan struct{}
	Exportable(id string) error
}

// sessionsDebounce collects bursts of changes into one hint.
var sessionsDebounce = 250 * time.Millisecond

func (s *Service) sessions(ctx context.Context, m protocol.Message) (json.RawMessage, error) {
	if s.Sessions == nil {
		return nil, protocol.Err("unsupported", "Host keeps no history")
	}
	res, err := s.Sessions.HostCall(ctx, m.Method, m.Params)
	if err != nil {
		return nil, publicError(err)
	}
	return protocol.JSON(res), nil
}

// serveSessions sends the history's hint on a link: now and after every
// change, until the link ends.
func (s *Service) serveSessions(ctx context.Context, link *hostLink) {
	h := s.Sessions
	if h == nil {
		return
	}
	for {
		changed := h.Changes()
		if link.conn.WriteEvent(ctx, agentlink.Event{Sessions: h.HostHint()}) != nil {
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
		case <-time.After(sessionsDebounce):
		}
	}
}
