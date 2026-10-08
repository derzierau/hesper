package transport_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/internal/perf"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// The latency harness (part R): the two-hesperd world (an in-process relay
// behind a tap that delays every message by one network leg, L and M with
// device keys and the end-to-end channel). It measures what a person at L
// feels with an agent on M, through L's own socket exactly as the app
// uses it (`hesperd attach M/x`):
//
//   - keystroke → echo through a remote attach (L's socket → L's bridge →
//     the link, a sealed relay stream → M's PTY, a fake shell that echoes
//     → back), over the relay and, where the firewall allows, the direct
//     path;
//   - opening a remote attach until its first frame;
//   - a state change on M until L's agents.subscribe shows it;
//   - a signed request through the channel (agents.list) and a ping;
//   - M's own time per stage (internal/perf).
//
// `make latency` (`go test -run '^TestLatency$' -bench Latency
// ./internal/transport`) prints the table with 20 ms legs
// (HESPER_LATENCY_LEG=40ms changes the round trip of each leg,
// HESPER_LATENCY_KEYS the keystrokes); plain `go test` runs a short
// regression check without delay.

type latencyRun struct {
	leg      time.Duration // round trip of each client ↔ relay leg
	keys     int
	requests int
	opens    int
	direct   bool
}

type latencyReport struct {
	run                           latencyRun
	open, echo, events, req, ping []time.Duration
	route                         string
}

func measureLatency(t *testing.T, run latencyRun) latencyReport {
	t.Helper()
	perf.Host.Reset()
	perf.Host.Enable(true)
	t.Cleanup(func() { perf.Host.Enable(false) })
	w := newWorld(t, worldOptions{delay: run.leg / 2, shell: true, direct: run.direct})
	a := spawnShell(t, w)
	if run.direct {
		deadline := time.Now().Add(15 * time.Second)
		for w.L.d.Fleet.Route("M") != "direct" && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
	}
	report := latencyReport{run: run, route: w.L.d.Fleet.Route("M")}
	open := func(mode string) *wire.AttachConn {
		t.Helper()
		started := time.Now()
		c := attach(t, w.L, wire.AttachRequest{Attach: a.ID, Mode: mode})
		if _, _, err := c.ReadFrame(); err != nil {
			t.Fatal(err)
		}
		report.open = append(report.open, time.Since(started))
		return c
	}
	for range run.opens - 1 {
		open(wire.ModeRO).Close()
	}
	c := open(wire.ModeRW)
	frames := make(chan []byte, 1024)
	go func() {
		defer close(frames)
		for {
			typ, p, err := c.ReadFrame()
			if err != nil {
				return
			}
			if typ == wire.FrameData {
				frames <- bytes.Clone(p)
			}
		}
	}()
	drain := func(quiet time.Duration) {
		for {
			select {
			case _, ok := <-frames:
				if !ok {
					t.Fatal("attach ended")
				}
			case <-time.After(quiet):
				return
			}
		}
	}
	drain(100 * time.Millisecond)
	// Echo: one random letter at a time, after the previous echo arrived;
	// Enter now and then so the line never wraps.
	for i := range run.keys {
		if i > 0 && i%40 == 0 {
			c.Input([]byte("\r"))
			drain(50 * time.Millisecond)
		}
		key := []byte{byte('a' + rand.IntN(26))}
		started := time.Now()
		if err := c.Input(key); err != nil {
			t.Fatal(err)
		}
		for got := false; !got; {
			select {
			case b, ok := <-frames:
				if !ok {
					t.Fatal("attach ended")
				}
				got = bytes.Contains(b, key)
			case <-time.After(5 * time.Second):
				t.Fatalf("no echo of %q", key)
			}
		}
		report.echo = append(report.echo, time.Since(started))
		drain(5 * time.Millisecond)
	}
	// State changes on M (a rename is the cheapest) until L's
	// subscription shows them.
	sub := w.L.client(t)
	if err := sub.Call(w.ctx, "agents.subscribe", nil, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	for len(sub.Notifications()) > 0 {
		<-sub.Notifications()
	}
	m := w.M.client(t)
	local := localOn(a.ID, "M")
	for i := range run.requests {
		name := "n" + strconv.Itoa(i)
		started := time.Now()
		if err := m.Call(w.ctx, "agents.rename", wire.RenameParams{ID: local, Name: name}, nil); err != nil {
			t.Fatal(err)
		}
		for arrived := false; !arrived; {
			select {
			case n := <-sub.Notifications():
				var ch wire.Changed
				json.Unmarshal(n.Params, &ch)
				arrived = ch.Agent.ID == a.ID && ch.Agent.Name == name
			case <-time.After(5 * time.Second):
				t.Fatal("no event")
			}
		}
		report.events = append(report.events, time.Since(started))
	}
	// Requests through the channel, paced as a person's would be (the
	// relay allows 60 messages a second per connection).
	fleet := w.L.d.Fleet
	for range run.requests {
		time.Sleep(20 * time.Millisecond)
		started := time.Now()
		if _, err := fleet.Call(w.ctx, "M", "agents.list", nil); err != nil {
			t.Fatal(err)
		}
		report.req = append(report.req, time.Since(started))
	}
	var hello wire.HelloResult
	for range run.requests {
		time.Sleep(20 * time.Millisecond)
		started := time.Now()
		if _, err := fleet.Call(w.ctx, "M", "ping", nil); err != nil {
			t.Fatal(err)
		}
		report.ping = append(report.ping, time.Since(started))
	}
	w.L.call(t, "hello", nil, &hello)
	return report
}

func ms(d time.Duration) string {
	return strconv.FormatFloat(float64(d)/float64(time.Millisecond), 'f', 1, 64)
}

func (r latencyReport) table() string {
	var b strings.Builder
	fmt.Fprintf(&b, "\nlatency: relay legs %s round trip each (network floor %s per keystroke over the relay), link route %s, e2e, software keys, fake shell echoing\n",
		r.run.leg, 2*r.run.leg, r.route)
	fmt.Fprintf(&b, "%-38s %5s %8s %8s %8s %8s\n", "measure (ms)", "n", "p50", "p95", "p99", "max")
	row := func(name string, s perf.Stats) {
		if s.N > 0 {
			fmt.Fprintf(&b, "%-38s %5d %8s %8s %8s %8s\n", name, s.N, ms(s.P50), ms(s.P95), ms(s.P99), ms(s.Max))
		}
	}
	row("keystroke → echo (remote attach)", perf.Summarize(r.echo))
	row("open remote attach → first frame", perf.Summarize(r.open))
	row("state change on M → L's subscribe", perf.Summarize(r.events))
	row("request agents.list (signed, e2e)", perf.Summarize(r.req))
	row("ping (e2e)", perf.Summarize(r.ping))
	for _, stage := range perf.Host.Stages() {
		row("hosts "+stage, perf.Host.Summary(stage))
	}
	return b.String()
}

func benchmarking() bool {
	f := flag.Lookup("test.bench")
	return f != nil && f.Value.String() != ""
}

// TestLatency guards against big regressions (generous bounds, no network
// delay); with -bench it is the harness and prints the tables (relay, and
// the direct path).
func TestLatency(t *testing.T) {
	if benchmarking() {
		leg := 20 * time.Millisecond
		if v, err := time.ParseDuration(os.Getenv("HESPER_LATENCY_LEG")); err == nil {
			leg = v
		}
		keys := 300
		if v, err := strconv.Atoi(os.Getenv("HESPER_LATENCY_KEYS")); err == nil && v > 0 {
			keys = v
		}
		fmt.Print(measureLatency(t, latencyRun{leg: leg, keys: keys, requests: 40, opens: 5}).table())
		fmt.Print(measureLatency(t, latencyRun{leg: leg, keys: keys, requests: 40, opens: 5, direct: true}).table())
		return
	}
	r := measureLatency(t, latencyRun{keys: 60, requests: 20, opens: 3})
	t.Log(r.table())
	for _, c := range []struct {
		name  string
		got   time.Duration
		limit time.Duration
	}{
		{"echo p50", perf.Summarize(r.echo).P50, 15 * time.Millisecond},
		{"echo p95", perf.Summarize(r.echo).P95, 60 * time.Millisecond},
		{"open max", perf.Summarize(r.open).Max, 3 * time.Second},
		{"events p95", perf.Summarize(r.events).P95, 100 * time.Millisecond},
		{"request p95", perf.Summarize(r.req).P95, 300 * time.Millisecond},
		{"ping p95", perf.Summarize(r.ping).P95, 150 * time.Millisecond},
	} {
		if c.got > c.limit {
			t.Errorf("%s %s exceeds %s", c.name, c.got, c.limit)
		}
	}
}
