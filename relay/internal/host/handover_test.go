package host

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

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

// handoverRelay acts like the relay for one host: every refresh issues an
// access token that lasts lifetime, a connection closes when its token
// expires, and a new connection replaces the device's previous one
// (relay.Hub.Connect).
type handoverRelay struct {
	lifetime  time.Duration
	refreshes atomic.Int32
	mu        sync.Mutex
	expiry    map[string]time.Time
	current   *websocket.Conn
	events    []string
}

func (f *handoverRelay) record(event string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event)
}

func (f *handoverRelay) log() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.events)
}

func (f *handoverRelay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/v1/auth/refresh":
		n := f.refreshes.Add(1)
		c := protocol.Credentials{DeviceID: "laptop", Role: "host", Token: fmt.Sprintf("access-%d", n), RefreshToken: fmt.Sprintf("refresh-%d", n), ExpiresAt: time.Now().Add(f.lifetime), RefreshExpiresAt: time.Now().Add(time.Hour)}
		f.mu.Lock()
		f.expiry[c.Token] = c.ExpiresAt
		f.mu.Unlock()
		json.NewEncoder(w).Encode(c)
	case "/v1/connect":
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		f.mu.Lock()
		expires, ok := f.expiry[token]
		f.mu.Unlock()
		if !ok || time.Now().After(expires) {
			http.Error(w, "expired", 401)
			return
		}
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{protocol.Subprotocol}})
		if err != nil {
			return
		}
		defer c.CloseNow()
		f.mu.Lock()
		if f.current != nil {
			f.current.CloseNow()
		}
		f.current = c
		f.events = append(f.events, "open "+token)
		f.mu.Unlock()
		ctx, cancel := context.WithDeadline(r.Context(), expires)
		defer cancel()
		for ctx.Err() == nil {
			if _, _, err := c.Read(ctx); err != nil {
				break
			}
		}
		c.CloseNow()
		f.record("close " + token)
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The host renews its access credential before the relay closes the
// connection at expiry: a second connection is up before the first one
// closes, and the links opened on the first one outlive it.
func TestHostRenewsBeforeExpiryWithoutDroppingLinks(t *testing.T) {
	ahead := renewAhead
	renewAhead = 2 * time.Second
	defer func() { renewAhead = ahead }()
	fake := &handoverRelay{lifetime: 3 * time.Second, expiry: map[string]time.Time{}}
	server := httptest.NewServer(fake)
	defer server.Close()
	path, _ := expiringHostFile(t, server.URL)
	logs := &lockedBuffer{}
	runner := &Runner{Service: &Service{}, CredentialsPath: path, Credentials: protocol.Credentials{Role: "host"}, PollInterval: time.Hour, Logger: slog.New(slog.NewTextHandler(logs, nil))}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()

	waitFor(t, "the first connection", func() bool { return slices.Contains(fake.log(), "open access-1") })
	var links *terminalManager
	waitFor(t, "the relay link manager", func() bool { links = runner.relayLinks.Load(); return links != nil })
	// A link opened now derives its context from the manager's (openLink).
	link, closeLink := context.WithCancel(links.ctx)
	defer closeLink()

	waitFor(t, "the old connection to close", func() bool { return slices.Contains(fake.log(), "close access-1") })
	events := fake.log()
	opened, closed := slices.Index(events, "open access-2"), slices.Index(events, "close access-1")
	if opened < 0 || opened > closed {
		t.Fatalf("old connection closed before the new one was up: %v", events)
	}
	time.Sleep(100 * time.Millisecond) // the old connection's Serve returns
	if strings.Contains(logs.String(), "host disconnected") {
		t.Fatalf("renewal dropped the host:\n%s", logs)
	}
	if link.Err() != nil {
		t.Fatal("a link ended with the hub connection it was opened on")
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if link.Err() == nil {
		t.Fatal("links outlived the host")
	}
}
