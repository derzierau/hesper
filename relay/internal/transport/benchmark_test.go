package transport

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/internal/identity"
	"github.com/derzierau/hesper/relay/internal/relay"
	"github.com/derzierau/hesper/relay/pkg/protocol"
	"github.com/derzierau/hesper/relay/pkg/session"
)

// encodingPeer queues then serializes once, as the production writer does.
// Socket and TLS costs are excluded.
// Synchronous draining prevents drops from appearing as artificially fast work.
type encodingPeer struct {
	p     wsPeer
	bytes int64
}

func (p *encodingPeer) Send(m protocol.Message) bool {
	if !p.p.Send(m) {
		return false
	}
	data, err := json.Marshal(<-p.p.queue)
	p.bytes += int64(len(data))
	return err == nil
}
func (*encodingPeer) Close() {}
func newEncodingPeer(b *testing.B) *encodingPeer {
	return &encodingPeer{p: wsPeer{queue: make(chan protocol.Message, 16), cancel: func() { b.Fatal("benchmark exceeded transport limits") }}}
}

func BenchmarkInventoryBroadcast(b *testing.B) {
	for _, hosts := range []int{1, 10, 50} {
		for _, controllers := range []int{1, 3} {
			b.Run(fmt.Sprintf("hosts=%d/controllers=%d", hosts, controllers), func(b *testing.B) {
				var devices []identity.Device
				for i := 0; i < hosts; i++ {
					devices = append(devices, identity.Device{ID: fmt.Sprintf("host-%d", i), Owner: "owner", Role: identity.Host, Name: fmt.Sprintf("Machine %d", i)})
				}
				for i := 0; i < controllers; i++ {
					devices = append(devices, identity.Device{ID: fmt.Sprintf("client-%d", i), Owner: "owner", Role: identity.Controller})
				}
				h := relay.New(devices, time.Second)
				defer h.Close()
				state := session.Snapshot{RuntimeID: strings.Repeat("a", 64), Capabilities: session.Capabilities{Agents: true, Ping: true}}
				for i := 0; i < 20; i++ {
					state.Terminals = append(state.Terminals, session.Terminal{
						Target: session.Target{RuntimeID: state.RuntimeID, TerminalID: fmt.Sprintf("agent-%d", i)},
						Role:   "claude", State: "working", Columns: 100, Rows: 40,
					})
				}
				message := protocol.Message{Version: protocol.Version, Type: "snapshot", Result: protocol.JSON(state)}
				var connections []*relay.Connection
				for _, device := range devices[:hosts] {
					c, err := h.Connect(device, newEncodingPeer(b))
					if err != nil {
						b.Fatal(err)
					}
					connections = append(connections, c)
					if err := h.Handle(c, message); err != nil {
						b.Fatal(err)
					}
				}
				var peers []*encodingPeer
				for _, device := range devices[hosts:] {
					p := newEncodingPeer(b)
					if _, err := h.Connect(device, p); err != nil {
						b.Fatal(err)
					}
					peers = append(peers, p)
				}
				for _, p := range peers {
					p.bytes = 0
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if err := h.Handle(connections[i%hosts], message); err != nil {
						b.Fatal(err)
					}
				}
				b.StopTimer()
				var total int64
				for _, p := range peers {
					total += p.bytes
				}
				b.ReportMetric(float64(total)/float64(b.N), "wire-B/update")
			})
		}
	}
}
