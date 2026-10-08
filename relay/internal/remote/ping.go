package remote

import (
	"context"
	"slices"
	"time"

	"github.com/derzierau/hesper/relay/pkg/client"
)

// Round trips (hello's machines[].rttMs): a ping (unsigned, inside the
// channel) to every other online machine every pingEvery over the route it
// is reached by; the median of the last rttWindow on that route.
const (
	pingEvery   = 10 * time.Second
	pingTimeout = 5 * time.Second
	rttWindow   = 5
)

func (f *Fleet) ping(ctx context.Context, c *client.Controller) {
	f.mu.Lock()
	var targets []*machine
	for _, m := range f.machines {
		if m.online && m.state.Capabilities.Ping {
			targets = append(targets, m)
		}
	}
	f.mu.Unlock()
	for _, m := range targets {
		if ctx.Err() != nil {
			return
		}
		f.pingOne(c, m)
	}
}

func (f *Fleet) pingOne(c *client.Controller, m *machine) {
	ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
	defer cancel()
	var how client.E2EReport
	started := time.Now()
	_, err := c.Request(client.WithE2EReport(ctx, &how), m.id, "ping", struct{}{})
	rtt := time.Since(started)
	if err != nil {
		return
	}
	route := how.Route
	if route == "" {
		route = client.RouteRelay
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if m.rttRoute != route {
		m.rtts, m.rttRoute = nil, route
	}
	m.rtts = append(m.rtts, rtt)
	if len(m.rtts) > rttWindow {
		m.rtts = m.rtts[len(m.rtts)-rttWindow:]
	}
}

func median(list []time.Duration) time.Duration {
	if len(list) == 0 {
		return 0
	}
	s := slices.Clone(list)
	slices.Sort(s)
	return s[len(s)/2]
}
