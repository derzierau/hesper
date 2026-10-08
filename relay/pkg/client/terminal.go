package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/derzierau/hesper/relay/pkg/direct"
	"github.com/derzierau/hesper/relay/pkg/e2e"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

// Terminal is one disposable terminal attachment. Lost connections are never
// resumed with buffered input. Open a new attachment for a fresh redraw.
//
// A stream opened through the end-to-end channel seals every frame
// (pkg/e2e Stream): Read returns terminal bytes as binary messages and, on
// the host, a resize as a text message with the resize control, exactly as
// a plaintext stream does; anything unsealed ends the stream.
type Terminal struct {
	// ID is the stream's ticket ID (OpenStream).
	ID            string
	conn          *websocket.Conn
	Columns, Rows uint16
	stream        *e2e.Stream
	writeMu       sync.Mutex // Seal and Write in the same order
	// dconn: a stream on the direct path (pkg/direct), end to end by its
	// own Noise handshake; pong receives its ping answers.
	dconn *direct.Conn
	pong  chan struct{}
	// Timing, when set, gets how long each frame took to open (seal
	// false) or to seal (seal true) on a sealed stream; the host's
	// latency harness reads it.
	Timing func(seal bool, d time.Duration)
}

// NewDirectTerminal is a terminal stream on a direct connection (both
// sides use it: the controller to type, the host to show).
func NewDirectTerminal(c *direct.Conn) *Terminal {
	return &Terminal{dconn: c, pong: make(chan struct{}, 1)}
}

// Direct reports whether the stream runs on the direct path.
func (t *Terminal) Direct() bool { return t.dconn != nil }

// EnableE2E seals this stream with the secret the host chose for it
// (host is true on the host's side).
func (t *Terminal) EnableE2E(secret []byte, streamID string, host bool) error {
	s, err := e2e.NewStream(secret, streamID, host)
	if err != nil {
		return err
	}
	t.stream = s
	return nil
}

// E2E reports whether the stream is end-to-end encrypted (sealed frames
// on the relay, or the direct path).
func (t *Terminal) E2E() bool { return t.stream != nil || t.dconn != nil }

func DialTerminal(ctx context.Context, credentials protocol.Credentials, ticket, controller string) (*Terminal, error) {
	u, err := Origin(credentials.Relay)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = "/v1/terminal"
	// wire names: kept as "X-Ghosty-Stream" and "X-Ghosty-Controller" until
	// the next relay deploy.
	headers := http.Header{"Authorization": []string{"Bearer " + credentials.Token}, "X-Ghosty-Stream": []string{ticket}}
	if controller != "" {
		headers.Set("X-Ghosty-Controller", controller)
	}
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, res, err := websocket.Dial(dialCtx, u.String(), &websocket.DialOptions{HTTPHeader: headers, Subprotocols: []string{protocol.TerminalProtocol}, HTTPClient: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}})
	if err != nil {
		if res != nil && (res.StatusCode == 401 || res.StatusCode == 403) {
			return nil, protocol.Err("unauthorized", "Terminal credential rejected")
		}
		return nil, err
	}
	if conn.Subprotocol() != protocol.TerminalProtocol {
		conn.CloseNow()
		return nil, fmt.Errorf("relay does not support live terminals")
	}
	conn.SetReadLimit(protocol.TerminalChunk)
	t := &Terminal{conn: conn}
	kind, data, err := t.Read(dialCtx)
	if err != nil {
		t.Close()
		return nil, err
	}
	var ready protocol.TerminalControl
	if kind != websocket.MessageText || json.Unmarshal(data, &ready) != nil || ready.Type != "ready" {
		t.Close()
		return nil, fmt.Errorf("terminal registration rejected")
	}
	return t, nil
}

// OpenStream asks a host for a stream (method answers a
// protocol.TerminalTicket: hesperd's agents.link) and connects it. Through
// the end-to-end channel the host returns the stream's secret inside the
// channel and the stream is sealed; with RequireE2E(ctx) a plaintext
// stream is refused. A ticket for the direct path is connected there (and
// opened again through the relay should that fail).
func (c *Controller) OpenStream(ctx context.Context, credentials protocol.Credentials, machine, method string, params any) (*Terminal, error) {
	var how E2EReport
	data, err := c.Request(WithE2EReport(ctx, &how), machine, method, params)
	report(ctx, how.Used, how.Require)
	reportRoute(ctx, how.Route, how.Addr)
	if err != nil {
		return nil, err
	}
	var ticket protocol.TerminalTicket
	if json.Unmarshal(data, &ticket) != nil || !protocol.ValidID(ticket.ID) {
		return nil, fmt.Errorf("invalid stream ticket")
	}
	if ticket.Direct {
		t, err := c.openDirectStream(ctx, machine, how.Addr, ticket.ID)
		if err != nil {
			if viaRelay(ctx) {
				return nil, err
			}
			return c.OpenStream(ViaRelay(ctx), credentials, machine, method, params)
		}
		t.ID, t.Columns, t.Rows = ticket.ID, ticket.Columns, ticket.Rows
		return t, nil
	}
	if len(ticket.E2E) == 0 && (how.Used || requiresE2E(ctx)) {
		return nil, protocol.Err(e2e.CodeRequired, "The host opened the stream without end-to-end encryption; refusing it")
	}
	t, err := DialTerminal(ctx, credentials, ticket.ID, "")
	if err == nil && len(ticket.E2E) > 0 {
		if err = t.EnableE2E(ticket.E2E, ticket.ID, false); err != nil {
			t.Close()
		}
	}
	if err == nil {
		t.ID, t.Columns, t.Rows = ticket.ID, ticket.Columns, ticket.Rows
	}
	return t, err
}

// Read returns the next message: terminal bytes (binary) or a control
// (text). On a sealed stream it opens the frame; a frame that does not
// authenticate ends the stream.
func (t *Terminal) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	if t.dconn != nil {
		return t.readDirect(ctx)
	}
	kind, data, err := t.conn.Read(ctx)
	if err != nil || t.stream == nil {
		return kind, data, err
	}
	fail := func(err error) (websocket.MessageType, []byte, error) {
		t.conn.CloseNow()
		return kind, nil, err
	}
	if kind != websocket.MessageBinary {
		return fail(e2e.ErrStream)
	}
	started := time.Now()
	frame, payload, err := t.stream.Open(data)
	if t.Timing != nil {
		t.Timing(false, time.Since(started))
	}
	if err != nil {
		return fail(err)
	}
	switch frame {
	case e2e.StreamData:
		return websocket.MessageBinary, payload, nil
	case e2e.StreamResize:
		columns, rows, err := e2e.ParseResize(payload)
		if err != nil {
			return fail(err)
		}
		return websocket.MessageText, protocol.JSON(protocol.TerminalControl{Type: "resize", Columns: columns, Rows: rows}), nil
	}
	return fail(e2e.ErrStream)
}
func (t *Terminal) Write(ctx context.Context, data []byte) error {
	if t.dconn != nil {
		for len(data) > 0 {
			n := min(len(data), protocol.TerminalChunk)
			if err := t.dconn.WriteMessage(ctx, direct.KindData, data[:n]); err != nil {
				return err
			}
			data = data[n:]
		}
		return nil
	}
	chunk := protocol.TerminalChunk
	if t.stream != nil {
		chunk -= e2e.StreamOverhead
	}
	for len(data) > 0 {
		n := min(len(data), chunk)
		if err := t.send(ctx, websocket.MessageBinary, e2e.StreamData, data[:n]); err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}
func (t *Terminal) Resize(ctx context.Context, columns, rows uint16) error {
	if t.dconn != nil {
		return t.dconn.WriteMessage(ctx, direct.KindResize, e2e.ResizePayload(columns, rows))
	}
	if t.stream != nil {
		return t.send(ctx, websocket.MessageBinary, e2e.StreamResize, e2e.ResizePayload(columns, rows))
	}
	return t.send(ctx, websocket.MessageText, 0, protocol.JSON(protocol.TerminalControl{Type: "resize", Columns: columns, Rows: rows}))
}
func (t *Terminal) send(ctx context.Context, kind websocket.MessageType, frame byte, data []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if t.stream == nil {
		return t.conn.Write(ctx, kind, data)
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	started := time.Now()
	sealed := t.stream.Seal(frame, data)
	if t.Timing != nil {
		t.Timing(true, time.Since(started))
	}
	return t.conn.Write(ctx, websocket.MessageBinary, sealed)
}
func (t *Terminal) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if t.dconn != nil {
		// Answered while someone reads the stream.
		if err := t.dconn.WriteMessage(ctx, direct.KindPing, []byte("ping")); err != nil {
			return err
		}
		select {
		case <-t.pong:
			return nil
		case <-ctx.Done():
			t.dconn.Close()
			return ctx.Err()
		}
	}
	return t.conn.Ping(ctx)
}
func (t *Terminal) Close() error {
	if t.dconn != nil {
		return t.dconn.Close()
	}
	return t.conn.CloseNow()
}

// readDirect returns the next message of a direct stream as Read does for
// a relay stream: terminal bytes as binary, a resize as a text control.
func (t *Terminal) readDirect(ctx context.Context) (websocket.MessageType, []byte, error) {
	stop := context.AfterFunc(ctx, func() { t.dconn.Close() })
	defer stop()
	for {
		kind, body, err := t.dconn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return 0, nil, ctx.Err()
			}
			return 0, nil, err
		}
		switch kind {
		case direct.KindData:
			return websocket.MessageBinary, body, nil
		case direct.KindResize:
			columns, rows, err := e2e.ParseResize(body)
			if err != nil {
				t.dconn.Close()
				return 0, nil, err
			}
			return websocket.MessageText, protocol.JSON(protocol.TerminalControl{Type: "resize", Columns: columns, Rows: rows}), nil
		case direct.KindPing:
			if len(body) > 64 {
				t.dconn.Close()
				return 0, nil, errors.New("direct: invalid ping")
			}
			if err := t.dconn.WriteMessage(ctx, direct.KindPong, body); err != nil {
				return 0, nil, err
			}
		case direct.KindPong:
			select {
			case t.pong <- struct{}{}:
			default:
			}
		default:
			t.dconn.Close()
			return 0, nil, errors.New("direct: unexpected message on a terminal stream")
		}
	}
}
