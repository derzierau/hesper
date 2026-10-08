// Package agentlink is the link between a controller's hesperd and a host's
// hesperd (rebuild contract part R): one terminal stream per controller and
// host (sealed through the relay, or a direct-path stream), multiplexed:
//
//   - channel 0 carries the host's agent events (its full list when the
//     link opens, then every change and removal) to the controller;
//   - every other channel carries one attach connection, byte for byte the
//     local attach protocol (the reply line, then frames) in both
//     directions, opened by a signed agents.attach request that names the
//     link and the channel.
//
// One stream for everything keeps within the relay's four streams per host
// and opens an attach in one round trip.
//
// Frame: kind (1 byte) | channel (uint32 BE) | length (uint32 BE) | payload.
package agentlink

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/coder/websocket"
	"github.com/derzierau/hesper/relay/pkg/client"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Frame kinds.
const (
	KindEvent byte = 'E' // host→controller, channel 0: JSON Event
	KindData  byte = 'D' // channel > 0, both: attach bytes
	KindClose byte = 'X' // channel > 0, both: the attach ended
)

// MaxPayload bounds a frame's payload.
const MaxPayload = 1 << 20

const header = 9

// Event is a channel-0 message: the first one carries Machine (the host's
// own short name) and the full List; later ones one Changed agent or one
// Removed id.
type Event struct {
	Machine string       `json:"machine,omitempty"`
	List    []wire.Agent `json:"list,omitempty"`
	Full    bool         `json:"full,omitempty"`
	Changed *wire.Agent  `json:"changed,omitempty"`
	Removed string       `json:"removed,omitempty"`
	// Reason (closing agents) is why Removed left: wire.ReasonClosed, …
	Reason string `json:"reason,omitempty"`
	// To (move work): with reason "moved", the agent Removed became (as
	// the controller that moved it named it).
	To string `json:"to,omitempty"`
	// Projects (projects step 1) is the host's shared project state
	// (internal/projects.State): sent when the link opens and after each
	// change; an event with Projects carries nothing else.
	Projects json.RawMessage `json:"projects,omitempty"`
	// Sessions (shared history) is the host's history hint
	// (internal/sessions.Hint: node, newest sequence number): sent when
	// the link opens and after changes; the controller pulls when it is
	// behind. An event with Sessions carries nothing else.
	Sessions json.RawMessage `json:"sessions,omitempty"`
}

// Stream is what a link runs on: a terminal stream's messages.
type Stream interface {
	Read(ctx context.Context) (websocket.MessageType, []byte, error)
	Write(ctx context.Context, data []byte) error
	Close() error
}

var _ Stream = (*client.Terminal)(nil)

// Conn frames a Stream. Writes are serialized; reads belong to one
// goroutine.
type Conn struct {
	s   Stream
	wmu sync.Mutex
	buf []byte
}

// New frames s.
func New(s Stream) *Conn { return &Conn{s: s} }

// Close closes the stream.
func (c *Conn) Close() error { return c.s.Close() }

// Write sends one frame.
func (c *Conn) Write(ctx context.Context, kind byte, ch uint32, payload []byte) error {
	if len(payload) > MaxPayload {
		return errors.New("agentlink: frame too large")
	}
	frame := make([]byte, header+len(payload))
	frame[0] = kind
	binary.BigEndian.PutUint32(frame[1:5], ch)
	binary.BigEndian.PutUint32(frame[5:9], uint32(len(payload)))
	copy(frame[header:], payload)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.s.Write(ctx, frame)
}

// WriteEvent sends an event on channel 0.
func (c *Conn) WriteEvent(ctx context.Context, e Event) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return c.Write(ctx, KindEvent, 0, data)
}

// Read returns the next frame; its payload is valid until the next Read.
func (c *Conn) Read(ctx context.Context) (kind byte, ch uint32, payload []byte, err error) {
	for {
		if len(c.buf) >= header {
			n := binary.BigEndian.Uint32(c.buf[5:9])
			if n > MaxPayload {
				return 0, 0, nil, fmt.Errorf("agentlink: frame of %d bytes", n)
			}
			if uint32(len(c.buf)-header) >= n {
				kind, ch = c.buf[0], binary.BigEndian.Uint32(c.buf[1:5])
				payload = append([]byte(nil), c.buf[header:header+int(n)]...)
				c.buf = c.buf[header+int(n):]
				if len(c.buf) == 0 {
					c.buf = nil
				}
				return kind, ch, payload, nil
			}
		}
		typ, data, err := c.s.Read(ctx)
		if err != nil {
			return 0, 0, nil, err
		}
		if typ != websocket.MessageBinary {
			continue // a resize control: links never send one
		}
		c.buf = append(c.buf, data...)
	}
}
