package agents

import (
	"encoding/json"
	"errors"
	"sync"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Routines (internal/routines): the registry opens the store and runs
// the scheduler (routinerun.go); the server routes routines.* to them and
// sends the store's changes to subscribers.

// routinesCall runs a routines.* method.
func (s *Server) routinesCall(method string, params json.RawMessage) (any, error) {
	store := s.reg.Routines()
	if store == nil {
		return nil, wire.Errorf(wire.CodeUnavailable, "routines are not available")
	}
	switch method {
	case "routines.list":
		return store.List(), nil
	case "routines.save":
		var p wire.RoutineSaveParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		r := p.Routine
		if r.Machine != "" && r.Machine != s.reg.machine {
			return nil, wire.Errorf(wire.CodeUnavailable, "routines on %s are not available yet (phase 2)", r.Machine)
		}
		if r.CreatedBy == "" {
			r.CreatedBy = "local"
		}
		return store.Save(r)
	case "routines.remove":
		var p wire.IDParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		if p.ID == "" {
			return nil, &badParams{errors.New("id is required")}
		}
		return struct{}{}, store.Remove(p.ID)
	case "routines.pause":
		var p wire.RoutinePauseParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		if p.ID == "" {
			return nil, &badParams{errors.New("id is required")}
		}
		return store.SetPaused(p.ID, p.Paused)
	case "routines.runs":
		var p wire.RoutineRunsParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		if p.ID == "" {
			return nil, &badParams{errors.New("id is required")}
		}
		return store.Runs(p.ID, p.Limit)
	case "routines.run":
		var p wire.IDParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		if p.ID == "" {
			return nil, &badParams{errors.New("id is required")}
		}
		if s.reg.routineSched == nil {
			return nil, wire.Errorf(wire.CodeUnavailable, "routines are not available")
		}
		// The daemon's lifetime bounds the run, not this call.
		return s.reg.routineSched.RunNow(s.reg.routineCtx, p.ID)
	}
	return nil, &noMethod{method}
}

// subscribeRoutines sends routines.changed / routines.removed on a
// subscribed control connection until it closes.
func (c *ctrl) subscribeRoutines(wg *sync.WaitGroup) {
	store := c.s.reg.Routines()
	if store == nil {
		return
	}
	sub := store.Subscribe()
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer store.Unsubscribe(sub)
		for {
			changed, removed := sub.Take()
			for _, r := range changed {
				params, _ := json.Marshal(wire.RoutineChanged{Routine: r})
				if c.write(wire.Notification{JSONRPC: "2.0", Method: wire.NoteRoutineChanged, Params: params}) != nil {
					return
				}
			}
			for _, id := range removed {
				params, _ := json.Marshal(wire.Removed{ID: id})
				if c.write(wire.Notification{JSONRPC: "2.0", Method: wire.NoteRoutineRemoved, Params: params}) != nil {
					return
				}
			}
			select {
			case <-sub.Wake():
			case <-c.done:
				return
			}
		}
	}()
}
