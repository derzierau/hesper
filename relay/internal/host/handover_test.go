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
	"github.com/derzierau/hesper/relay/pkg/client"
	"github.com/derzierau/hesper/relay/pkg/e2e"
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
// (relay.Hub.Connect). A non-zero familyEnd caps every token there, as the
// relay caps access tokens at the refresh family's expiry.
type handoverRelay struct {
	lifetime  time.Duration
	familyEnd time.Time
	// request, if set, is sent to the host on the first connection.
	request string
	// probeRenewed pings the host on every later connection and sends it a
	// request it answers without the operation lock (an end-to-end hello).
	probeRenewed bool
	refreshes    atomic.Int32
	mu           sync.Mutex
	expiry       map[string]time.Time
	current      *websocket.Conn
	events       []string
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
		if !f.familyEnd.IsZero() && f.familyEnd.Before(c.ExpiresAt) {
			c.ExpiresAt = f.familyEnd
		}
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
		if f.request != "" && token == "access-1" {
			c.Write(ctx, websocket.MessageText, protocol.JSON(protocol.Message{Version: protocol.Version, Type: "request", ID: "r1", Method: f.request}))
		}
		if f.probeRenewed && token != "access-1" {
			go func() {
				pingCtx, stop := context.WithTimeout(ctx, 2*time.Second)
				defer stop()
				if c.Ping(pingCtx) == nil {
					f.record("pong " + token)
				}
			}()
			c.Write(ctx, websocket.MessageText, protocol.JSON(protocol.Message{Version: protocol.Version, Type: "request", ID: "r2", Method: e2e.HelloMethod}))
		}
		for ctx.Err() == nil {
			_, data, err := c.Read(ctx)
			if err != nil {
				break
			}
			var m protocol.Message
			if json.Unmarshal(data, &m) == nil && (m.Type == "snapshot" || m.Type == "result") {
				f.record(m.Type + " " + token)
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
	ahead, retry := renewAhead, renewRetry
	renewAhead, renewRetry = 2*time.Second, 200*time.Millisecond
	defer func() { renewAhead, renewRetry = ahead, retry }()
	fake := &handoverRelay{lifetime: 3 * time.Second, expiry: map[string]time.Time{}}
	server := httptest.NewServer(fake)
	defer server.Close()
	path, _ := expiringHostFile(t, server.URL)
	logs := &lockedBuffer{}
	runner := &Runner{Service: &Service{}, CredentialsPath: path, Credentials: protocol.Credentials{Role: "host"}, PollInterval: time.Hour, Logger: slog.New(slog.NewTextHandler(logs, nil))}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Run's link manager, reachable here: it ends with run.
	runner.init()
	linkCtx, endLinks := context.WithCancel(ctx)
	links := &terminalManager{ctx: linkCtx, r: runner, slots: runner.termSlots}
	done := make(chan error, 1)
	go func() { defer endLinks(); done <- runner.run(ctx, links) }()

	waitFor(t, "the first connection", func() bool { return slices.Contains(fake.log(), "open access-1") })
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

// A renewed token with no more than renewAhead left never makes the host
// renew without pause: it waits at least renewRetry between attempts, and a
// token that expires no later than the current one ends renewal for the
// connection.
func TestHostRenewalPacesShortTokens(t *testing.T) {
	ahead, retry := renewAhead, renewRetry
	renewAhead, renewRetry = 2*time.Second, 300*time.Millisecond
	defer func() { renewAhead, renewRetry = ahead, retry }()
	for _, tc := range []struct {
		name string
		fake *handoverRelay
		max  int32
	}{
		// Every token lasts less than renewAhead (a short access TTL).
		{"short lifetime", &handoverRelay{lifetime: 1500 * time.Millisecond}, 8},
		// The refresh family ends soon: every token expires at its end.
		{"family end", &handoverRelay{lifetime: time.Hour, familyEnd: time.Now().Add(2500 * time.Millisecond)}, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.fake.expiry = map[string]time.Time{}
			server := httptest.NewServer(tc.fake)
			defer server.Close()
			path, _ := expiringHostFile(t, server.URL)
			runner := &Runner{Service: &Service{}, CredentialsPath: path, Credentials: protocol.Credentials{Role: "host"}, PollInterval: time.Hour, Logger: slog.New(slog.NewTextHandler(&lockedBuffer{}, nil))}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- runner.Run(ctx) }()
			waitFor(t, "the first connection", func() bool { return slices.Contains(tc.fake.log(), "open access-1") })
			time.Sleep(time.Second)
			cancel()
			<-done
			if n := tc.fake.refreshes.Load(); n > tc.max {
				t.Fatalf("%d refreshes in about a second, want at most %d", n, tc.max)
			}
		})
	}
}

// The renewed connection is served at once, even while an operation the
// old connection started is still running: the relay expects the host to
// answer on the new connection (pings, requests) from its first moment.
func TestHostServesRenewedConnectionWhileOldOneDrains(t *testing.T) {
	ahead, retry := renewAhead, renewRetry
	renewAhead, renewRetry = 2*time.Second, 200*time.Millisecond
	defer func() { renewAhead, renewRetry = ahead, retry }()
	fake := &handoverRelay{lifetime: 3 * time.Second, request: "agents.list", probeRenewed: true, expiry: map[string]time.Time{}}
	server := httptest.NewServer(fake)
	defer server.Close()
	path, _ := expiringHostFile(t, server.URL)
	runner := &Runner{Service: &Service{}, CredentialsPath: path, Credentials: protocol.Credentials{Role: "host"}, PollInterval: time.Hour, Logger: slog.New(slog.NewTextHandler(&lockedBuffer{}, nil))}
	// A long operation (from the direct path, say) holds the operation lock,
	// so the request sent on the first connection waits inside its serve.
	runner.init()
	runner.opMu.Lock()
	locked := true
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	defer func() {
		if locked {
			runner.opMu.Unlock()
			cancel()
			<-done
		}
	}()

	waitFor(t, "the renewed connection", func() bool { return slices.Contains(fake.log(), "open access-2") })
	deadline := time.Now().Add(time.Second)
	served := func() bool {
		events := fake.log()
		return slices.Contains(events, "snapshot access-2") && slices.Contains(events, "pong access-2") && slices.Contains(events, "result access-2")
	}
	for !served() {
		if time.Now().After(deadline) {
			t.Fatalf("renewed connection not served while the old one drains: %v", fake.log())
		}
		time.Sleep(10 * time.Millisecond)
	}
	runner.opMu.Unlock()
	locked = false
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// A serve that is replaced (its context ends) hands a state change it took
// but has not published on to the serve that replaces it, which publishes
// at once instead of on the next poll.
func TestReplacedServeHandsOnPendingChange(t *testing.T) {
	fake := &handoverRelay{expiry: map[string]time.Time{"t": time.Now().Add(time.Hour)}}
	server := httptest.NewServer(fake)
	defer server.Close()
	events := make(chan struct{}, 1)
	runner := &Runner{Service: &Service{}, Events: events, PollInterval: time.Hour, Logger: slog.New(slog.NewTextHandler(&lockedBuffer{}, nil))}
	runner.init()
	link, err := client.Dial(context.Background(), protocol.Credentials{Relay: server.URL, DeviceID: "laptop", Role: "host", Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	terminals := &terminalManager{ctx: ctx, r: runner, slots: runner.termSlots}
	go func() { done <- runner.serve(ctx, link, terminals) }()
	waitFor(t, "the first snapshot", func() bool { return slices.Contains(fake.log(), "snapshot t") })
	events <- struct{}{}
	// The serve takes the change and waits out PublishGap before publishing.
	waitFor(t, "the change to be taken", func() bool { return len(events) == 0 })
	stop()
	<-done
	select {
	case <-runner.changed:
	default:
		t.Fatal("a change taken by the replaced serve was lost")
	}
}
