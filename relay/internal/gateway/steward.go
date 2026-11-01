package gateway

import (
	"context"
	"encoding/json"
	"time"

	"github.com/derzierau/hesper/relay/internal/steward"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// newSteward is the Steward (internal/steward) on the loops store; it
// starts once the registry is open (wireSteward).
func (d *Daemon) newSteward(stateDir string, logf func(string, ...any)) *steward.Steward {
	return steward.New(steward.Options{StateDir: stateDir, Store: d.Loops, ProjectPath: d.Projects.PathOn, Logf: logf})
}

// wireSteward starts the Steward and feeds it what wakes it besides the
// registry's settled and closed events: its own agent's changes, reviews
// (the registry's review notes) and loops created after it started: from
// the store's creation hook with the calling agent's service (the
// Steward's own loops never wake it), and from its notifications (loops
// made by Assign or on another Mac; the Steward counts each loop once).
func (d *Daemon) wireSteward(ctx context.Context) {
	st, reg, store := d.Steward, d.Registry, d.Loops
	st.Start(ctx, reg)
	store.SetCreated(st.LoopCreated)
	sub := reg.SubscribeMoves() // review notes come only to these
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		defer reg.Unsubscribe(sub)
		for {
			notes := sub.Wait(ctx.Done())
			if notes == nil {
				return
			}
			for _, n := range notes {
				switch {
				case n.Agent != nil:
					st.AgentChanged(*n.Agent)
				case n.Review != "":
					if a, err := reg.Get(n.Review); err == nil && a.Service == "" {
						st.ReviewChanged(a.ID, a.Loop)
					}
				}
			}
		}
	}()
	started := time.Now()
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		store.Watch(ctx.Done(), func(method string, params any) error {
			if method != wire.NoteLoopChanged {
				return nil
			}
			if id, created := loopOf(params); id != "" && created.After(started) {
				st.LoopCreated(id, "")
			}
			return nil
		})
	}()
}

// loopOf is a loops.changed note's loop id and creation time (the
// store sends its JSON as it is).
func loopOf(params any) (string, time.Time) {
	data, ok := params.(json.RawMessage)
	if !ok {
		return "", time.Time{}
	}
	var note wire.LoopChanged
	if json.Unmarshal(data, &note) != nil {
		return "", time.Time{}
	}
	return note.Loop.ID, note.Loop.Created
}
