package transport_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/derzierau/hesper/relay/pkg/e2e"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

// relayTap stands where the relay operator stands: every WebSocket (the
// JSON connections and the terminal streams) of every client goes through
// it to the real relay. It records every frame and can alter frames on the
// way, as a compromised relay could.
type relayTap struct {
	server *httptest.Server
	mu     sync.Mutex
	frames [][]byte
	// tamperFrames flips a byte of the ciphertext in the next e2e request a
	// controller sends; tamperStream flips a byte in the next binary
	// terminal frame a controller sends.
	tamperFrames atomic.Int32
	tamperStream atomic.Int32
	tampered     atomic.Int32
	// hideE2E removes the e2e capability from inventories sent to
	// controllers (a relay trying to downgrade them).
	hideE2E atomic.Bool
	// streams counts terminal streams (/v1/terminal sockets) through the
	// relay.
	streams atomic.Int32
	// delay (nanoseconds) holds every message passing the tap for that
	// long in each direction: one network leg between a client and the
	// relay (the latency harness; half the leg's round trip).
	delay atomic.Int64
	// cuts ends every proxied socket (dropAll: the relay restarting).
	cuts map[*context.CancelFunc]bool
}

// dropAll closes every socket through the tap, as a relay restart does.
func (tap *relayTap) dropAll() {
	tap.mu.Lock()
	defer tap.mu.Unlock()
	for cut := range tap.cuts {
		(*cut)()
	}
}

func newRelayTap(t *testing.T, upstream string) *relayTap {
	t.Helper()
	target, _ := url.Parse(upstream)
	tap := &relayTap{}
	plain := httputil.NewSingleHostReverseProxy(target)
	tap.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/connect" && r.URL.Path != "/v1/terminal" {
			plain.ServeHTTP(w, r)
			return
		}
		ws := *target
		ws.Scheme, ws.Path = "ws", r.URL.Path
		headers := http.Header{}
		for _, h := range []string{"Authorization", "X-Ghosty-Stream", "X-Ghosty-Controller"} {
			if v := r.Header.Get(h); v != "" {
				headers.Set(h, v)
			}
		}
		protocols := strings.Split(r.Header.Get("Sec-WebSocket-Protocol"), ",")
		for i := range protocols {
			protocols[i] = strings.TrimSpace(protocols[i])
		}
		up, res, err := websocket.Dial(r.Context(), ws.String(), &websocket.DialOptions{HTTPHeader: headers, Subprotocols: protocols})
		if err != nil {
			status := http.StatusBadGateway
			if res != nil {
				status = res.StatusCode
			}
			w.WriteHeader(status)
			return
		}
		down, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{up.Subprotocol()}})
		if err != nil {
			up.CloseNow()
			return
		}
		if r.URL.Path == "/v1/terminal" {
			tap.streams.Add(1)
		}
		up.SetReadLimit(2 << 20)
		down.SetReadLimit(2 << 20)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		tap.mu.Lock()
		if tap.cuts == nil {
			tap.cuts = map[*context.CancelFunc]bool{}
		}
		tap.cuts[&cancel] = true
		tap.mu.Unlock()
		defer func() { tap.mu.Lock(); delete(tap.cuts, &cancel); tap.mu.Unlock() }()
		pump := func(src, dst *websocket.Conn, fromClient bool) {
			defer cancel()
			send := func(kind websocket.MessageType, data []byte) bool { return dst.Write(ctx, kind, data) == nil }
			if delay := time.Duration(tap.delay.Load()); delay > 0 {
				// A network leg: every message arrives delay later, in
				// order, without holding up the ones behind it.
				type delayed struct {
					kind websocket.MessageType
					data []byte
					at   time.Time
				}
				queue := make(chan delayed, 4096)
				defer close(queue)
				go func() {
					for m := range queue {
						time.Sleep(time.Until(m.at))
						if dst.Write(ctx, m.kind, m.data) != nil {
							cancel()
						}
					}
				}()
				send = func(kind websocket.MessageType, data []byte) bool {
					select {
					case queue <- delayed{kind, data, time.Now().Add(delay)}:
						return true
					case <-ctx.Done():
						return false
					}
				}
			}
			for {
				kind, data, err := src.Read(ctx)
				if err != nil {
					return
				}
				tap.mu.Lock()
				tap.frames = append(tap.frames, bytes.Clone(data))
				tap.mu.Unlock()
				if fromClient {
					data = tap.alter(r.URL.Path, kind, data)
				} else if tap.hideE2E.Load() && r.URL.Path == "/v1/connect" {
					data = bytes.ReplaceAll(data, []byte(`,"e2e":true`), nil)
				}
				if !send(kind, data) {
					return
				}
			}
		}
		go pump(up, down, false)
		pump(down, up, true)
		up.CloseNow()
		down.CloseNow()
	}))
	t.Cleanup(tap.server.Close)
	return tap
}

func (tap *relayTap) alter(path string, kind websocket.MessageType, data []byte) []byte {
	if path == "/v1/terminal" && kind == websocket.MessageBinary && tap.tamperStream.Load() > 0 {
		tap.tamperStream.Add(-1)
		tap.tampered.Add(1)
		out := bytes.Clone(data)
		out[len(out)/2] ^= 0x01
		return out
	}
	if path != "/v1/connect" || tap.tamperFrames.Load() == 0 {
		return data
	}
	var m protocol.Message
	if json.Unmarshal(data, &m) != nil || m.Type != "request" || m.Method != e2e.FrameMethod {
		return data
	}
	var f e2e.Frame
	if json.Unmarshal(m.Params, &f) != nil || len(f.C) == 0 {
		return data
	}
	tap.tamperFrames.Add(-1)
	tap.tampered.Add(1)
	f.C[len(f.C)/2] ^= 0x01
	m.Params = protocol.JSON(f)
	out, _ := json.Marshal(m)
	return out
}

// sawAny reports whether any recorded frame contains one of the needles,
// raw or in base64 (how JSON carries bytes).
func (tap *relayTap) sawAny(needles ...string) string {
	tap.mu.Lock()
	defer tap.mu.Unlock()
	for _, frame := range tap.frames {
		for _, n := range needles {
			if bytes.Contains(frame, []byte(n)) || bytes.Contains(frame, []byte(base64.StdEncoding.EncodeToString([]byte(n)))) {
				return n
			}
		}
	}
	return ""
}

func (tap *relayTap) count(needle string) int {
	tap.mu.Lock()
	defer tap.mu.Unlock()
	n := 0
	for _, frame := range tap.frames {
		if bytes.Contains(frame, []byte(needle)) {
			n++
		}
	}
	return n
}
