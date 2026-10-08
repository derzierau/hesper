package remote

import (
	"context"
	"encoding/json"
	"time"

	"github.com/derzierau/hesper/relay/internal/projects"
	"github.com/derzierau/hesper/relay/pkg/client"
)

// Projects (workspace model step 1): this Mac's project registry is
// replicated with every other machine's over the links. A host's state
// arrives on its link (link.read → projectsEvent); this Mac's goes to
// each linked machine with projects.sync (signed, through the channel)
// when a link opens and after every change, and the answer (the host's
// state) is merged too. Merging is idempotent, so the exchange settles
// after one round. Hosts without projects answer "unsupported": ignored.

// projectsSyncDebounce collects bursts of changes into one exchange.
var projectsSyncDebounce = 150 * time.Millisecond

// projectsEvent merges a host's state that came on its link.
func (f *Fleet) projectsEvent(l *link, raw json.RawMessage) {
	store := f.opt.Projects
	if store == nil {
		return
	}
	var st projects.State
	if json.Unmarshal(raw, &st) != nil {
		return
	}
	f.mu.Lock()
	current, short := l.m.link == l, l.m.short
	f.mu.Unlock()
	if current {
		store.Merge(&st, short)
	}
}

// syncProjects sends this Mac's state to one machine and merges its
// answer.
func (f *Fleet) syncProjects(c *client.Controller, m *machine) {
	store := f.opt.Projects
	if store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	raw, err := f.request(ctx, c, m, "projects.sync", map[string]any{"state": store.Export()})
	if err != nil {
		return // an older host, or gone: the next change or link tries again
	}
	var st projects.State
	if json.Unmarshal(raw, &st) == nil {
		f.mu.Lock()
		short := m.short
		f.mu.Unlock()
		store.Merge(&st, short)
	}
}

// syncProjectsLoop sends every change of this Mac's projects to the
// linked machines until ctx ends (one relay connection).
func (f *Fleet) syncProjectsLoop(ctx context.Context, c *client.Controller) {
	store := f.opt.Projects
	if store == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-store.Changes():
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(projectsSyncDebounce):
		}
		f.mu.Lock()
		var linked []*machine
		if f.c == c {
			for _, m := range f.machines {
				if m.link != nil {
					linked = append(linked, m)
				}
			}
		}
		f.mu.Unlock()
		for _, m := range linked {
			go f.syncProjects(c, m)
		}
	}
}
