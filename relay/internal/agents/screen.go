package agents

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/derzierau/hesper/relay/internal/vt"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Screen is an agent's terminal as plain text (agents.screen): its last
// rows, after scrollback lines when asked. An exited agent shows its last
// screen (blank after a daemon restart).
func (r *Registry) Screen(p wire.ScreenParams) (wire.ScreenResult, error) {
	if p.Rows < 0 || p.Scrollback < 0 || p.Scrollback > wire.MaxScreenScrollback {
		return wire.ScreenResult{}, wire.Errorf(wire.CodeInvalid, "rows and scrollback must be 0 to %d", wire.MaxScreenScrollback)
	}
	term, err := r.Term(p.ID)
	if err != nil {
		return wire.ScreenResult{}, err
	}
	var res wire.ScreenResult
	term.WithScreen(func(s *vt.Screen) {
		res.Cols, res.Rows = s.Size()
		res.Text = strings.Join(s.PlainLines(p.Rows, p.Scrollback), "\n")
		m := s.Modes()
		res.Alt = m.Alt
		if m.CursorVisible {
			x, y := s.Cursor()
			res.Cursor = &wire.Cursor{Col: x, Row: y}
		}
	})
	return res, nil
}

// screen serves agents.screen; forward sends it to another machine's
// daemon (dispatch's).
func (s *Server) screen(params json.RawMessage, forward func(string) (bool, any, error)) (any, error) {
	var p wire.ScreenParams
	if err := decode(params, &p); err != nil {
		return nil, err
	}
	if p.ID == "" {
		return nil, &badParams{errors.New("id is required")}
	}
	if ok, res, err := forward(p.ID); ok {
		return res, err
	}
	return s.reg.Screen(p)
}
