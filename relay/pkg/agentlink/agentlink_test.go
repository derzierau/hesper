package agentlink

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/coder/websocket"
)

// chunky delivers what was written in pieces of 7 bytes, plus a text
// message now and then (a resize control, which links ignore).
type chunky struct {
	data  []byte
	reads int
}

func (c *chunky) Write(_ context.Context, p []byte) error { c.data = append(c.data, p...); return nil }
func (c *chunky) Close() error                            { return nil }
func (c *chunky) Read(context.Context) (websocket.MessageType, []byte, error) {
	c.reads++
	if c.reads%5 == 0 {
		return websocket.MessageText, []byte(`{"type":"resize"}`), nil
	}
	if len(c.data) == 0 {
		return 0, nil, io.EOF
	}
	n := min(7, len(c.data))
	p := c.data[:n]
	c.data = c.data[n:]
	return websocket.MessageBinary, p, nil
}

func TestFramesSurviveAnyChunking(t *testing.T) {
	s := &chunky{}
	c := New(s)
	ctx := context.Background()
	big := bytes.Repeat([]byte("x"), 70000)
	c.WriteEvent(ctx, Event{Machine: "M", Full: true})
	c.Write(ctx, KindData, 7, []byte("hello"))
	c.Write(ctx, KindData, 9, big)
	c.Write(ctx, KindClose, 7, nil)
	want := []struct {
		kind byte
		ch   uint32
		n    int
	}{{KindEvent, 0, len(`{"machine":"M","full":true}`)}, {KindData, 7, 5}, {KindData, 9, len(big)}, {KindClose, 7, 0}}
	for i, w := range want {
		kind, ch, p, err := c.Read(ctx)
		if err != nil || kind != w.kind || ch != w.ch || len(p) != w.n {
			t.Fatalf("frame %d: %c %d %d %v", i, kind, ch, len(p), err)
		}
	}
	if _, _, _, err := c.Read(ctx); err != io.EOF {
		t.Fatalf("end: %v", err)
	}
	if err := c.Write(ctx, KindData, 1, make([]byte, MaxPayload+1)); err == nil {
		t.Fatal("an oversized frame was written")
	}
}
