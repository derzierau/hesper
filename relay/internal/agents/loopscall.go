package agents

import (
	"encoding/json"
	"strings"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// loopsDispatch runs a loops.*, goals.* or steward.* method of control
// connection c: who calls it first (an agent or a person), then the loops
// store or the Steward. The agent tree's rules for these methods (which
// are a person's only, what the Steward may do at its level) live here.
func (s *Server) loopsDispatch(c *ctrl, method string, params json.RawMessage) (any, error) {
	caller, t, err := s.callerOf(c, method, params)
	if err != nil {
		return nil, err
	}
	who := Caller{Agent: caller}
	if a, ok := t[caller]; ok {
		who.Service = a.Service
	}
	reg := s.reg
	if strings.HasPrefix(method, "steward.") {
		if reg.opt.Steward == nil {
			return nil, wire.Errorf(wire.CodeUnavailable, "the Steward is not available")
		}
		res, err, _ := reg.opt.Steward.Call(method, params, who)
		return res, err
	}
	if reg.opt.Loops == nil {
		return nil, wire.Errorf(wire.CodeUnavailable, "loops are not available")
	}
	res, err, _ := reg.opt.Loops.Call(method, params, who)
	return res, err
}
