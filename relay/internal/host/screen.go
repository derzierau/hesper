package host

import (
	"context"
	"encoding/json"

	"github.com/derzierau/hesper/relay/pkg/protocol"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// screen serves agents.screen (right observe): an agent's terminal as
// plain text. A shell the caller may not see does not exist for it.
func (s *Service) screen(ctx context.Context, m protocol.Message) (json.RawMessage, error) {
	var p wire.ScreenParams
	if err := params(m.Params, &p); err != nil {
		return nil, err
	}
	a, err := s.agent(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	p.ID = a.ID
	res, err := s.Agents.Screen(p)
	if err != nil {
		return nil, publicError(err)
	}
	return protocol.JSON(res), nil
}
