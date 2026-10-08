package transport_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/derzierau/hesper/relay/pkg/client"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

// fakeHost is a host connection for tests of the relay itself: it
// publishes whatever snapshot the test sets and answers requests with
// handle.
type fakeHost struct {
	link   *client.Link
	mu     sync.Mutex
	handle func(protocol.Message) (any, error)
	ctx    context.Context
}

func startFakeHost(t *testing.T, ctx context.Context, creds protocol.Credentials, snapshot any, handle func(protocol.Message) (any, error)) *fakeHost {
	t.Helper()
	link, err := client.Dial(ctx, creds)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(ctx)
	h := &fakeHost{link: link, handle: handle, ctx: ctx}
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); link.Close(); <-done })
	h.publish(snapshot)
	go func() {
		defer close(done)
		for {
			m, err := link.Read(ctx)
			if err != nil {
				return
			}
			if m.Type != "request" {
				continue
			}
			result, err := h.handle(m)
			answer := protocol.Message{Type: "result", ID: m.ID, Result: protocol.JSON(result)}
			if err != nil {
				answer.Result, answer.Error = nil, protocol.PublicError(err)
			}
			if link.Send(ctx, answer) != nil {
				return
			}
		}
	}()
	return h
}

func (h *fakeHost) publish(snapshot any) {
	data, _ := json.Marshal(snapshot)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.link.Send(h.ctx, protocol.Message{Type: "snapshot", Result: data})
}
