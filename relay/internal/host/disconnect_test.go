package host

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/derzierau/hesper/relay/pkg/client"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

// closingRelay accepts a host, reads its first snapshot and closes the
// connection as the relay does for a message over its limit.
func closingRelay(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{protocol.Subprotocol}})
		if err != nil {
			return
		}
		defer c.CloseNow()
		if _, _, err := c.Read(r.Context()); err != nil {
			return
		}
		c.Close(websocket.StatusMessageTooBig, "message too big")
	}))
	t.Cleanup(server.Close)
	return server
}

// lockedBuffer is a log destination the test reads while Run writes.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// The relay's reason for ending the session reaches the caller instead of
// the bare cancellation the reader caused on its way out.
func TestServeReturnsWhyTheRelayClosedTheConnection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	server := closingRelay(t)
	reg, _ := testRegistry(t)
	runner := &Runner{Service: &Service{Agents: reg}, PollInterval: time.Hour,
		Credentials: protocol.Credentials{Relay: server.URL, DeviceID: "mini-1", Role: "host", Token: "t"}}
	link, err := client.Dial(ctx, runner.Credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer link.Close()
	err = runner.Serve(ctx, link)
	if err == nil || errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "relay closed the connection") ||
		!strings.Contains(err.Error(), "message too big") || websocket.CloseStatus(err) != websocket.StatusMessageTooBig {
		t.Fatalf("Serve returned %v", err)
	}
}

// Stopping the host is not a dropped session: Serve says only that it was
// cancelled.
func TestServeStoppedByItsCallerReportsNoRelayFault(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{protocol.Subprotocol}})
		if err != nil {
			return
		}
		defer c.CloseNow()
		<-r.Context().Done()
	}))
	defer server.Close()
	reg, _ := testRegistry(t)
	runner := &Runner{Service: &Service{Agents: reg}, PollInterval: time.Hour,
		Credentials: protocol.Credentials{Relay: server.URL, DeviceID: "mini-1", Role: "host", Token: "t"}}
	link, err := client.Dial(ctx, runner.Credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer link.Close()
	serveCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- runner.Serve(serveCtx, link) }()
	time.Sleep(200 * time.Millisecond)
	stop()
	if err := <-done; !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "relay closed") {
		t.Fatalf("Serve returned %v", err)
	}
}

// Run's warning names the cause and the daemon remembers the drop for
// hesperctl status.
func TestRunLogsAndCountsWhyTheSessionDropped(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	server := closingRelay(t)
	reg, _ := testRegistry(t)
	logs := &lockedBuffer{}
	runner := &Runner{Service: &Service{Agents: reg}, PollInterval: time.Hour, Logger: slog.New(slog.NewTextHandler(logs, nil)),
		Credentials: protocol.Credentials{Relay: server.URL, DeviceID: "mini-1", Role: "host", Token: "t"}}
	if s := runner.RelaySession(); s != nil {
		t.Fatalf("relay session before any drop: %+v", s)
	}
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	for !strings.Contains(logs.String(), "host disconnected") {
		if ctx.Err() != nil {
			t.Fatal("no disconnect logged:", logs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if out := logs.String(); !strings.Contains(out, "relay closed the connection") || !strings.Contains(out, "message too big") {
		t.Fatalf("log does not say why:\n%s", out)
	}
	s := runner.RelaySession()
	if s == nil || s.Drops5m < 1 || !strings.Contains(s.LastReason, "message too big") || time.Since(s.LastAt) > time.Minute {
		t.Fatalf("relay session: %+v", s)
	}
}
