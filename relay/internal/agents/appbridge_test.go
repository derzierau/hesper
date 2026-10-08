package agents

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// fakeApp is Hesper.app's side of app control on a raw connection: it
// registers, then reads the requests hesperd sends it.
type fakeApp struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
}

func (h *harness) fakeApp() *fakeApp {
	h.t.Helper()
	conn, err := net.Dial("unix", h.sock)
	if err != nil {
		h.t.Fatal(err)
	}
	a := &fakeApp{t: h.t, conn: conn, r: bufio.NewReader(conn)}
	h.t.Cleanup(func() { conn.Close() })
	a.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "app.register", "params": map[string]any{}})
	var res wire.Response
	a.read(&res)
	if string(res.ID) != "1" || res.Error != nil {
		h.t.Fatalf("register: %+v", res)
	}
	return a
}

func (a *fakeApp) send(v any) {
	a.t.Helper()
	line, _ := json.Marshal(v)
	if _, err := a.conn.Write(append(line, '\n')); err != nil {
		a.t.Fatal(err)
	}
}

func (a *fakeApp) read(v any) {
	a.t.Helper()
	a.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := wire.ReadLine(a.r)
	if err != nil {
		a.t.Fatal(err)
	}
	if err := json.Unmarshal(line, v); err != nil {
		a.t.Fatalf("%s: %v", line, err)
	}
}

// request is the next request hesperd forwards.
func (a *fakeApp) request() wire.Request {
	a.t.Helper()
	var req wire.Request
	a.read(&req)
	return req
}

func callAsync(h *harness, method string, params any) chan error {
	done := make(chan error, 1)
	go func() {
		var out json.RawMessage
		err := h.call(method, params, &out)
		if err == nil && string(out) == "" {
			err = errors.New("empty result")
		}
		done <- err
	}()
	return done
}

func TestAppControlForwardsToRegisteredApp(t *testing.T) {
	h := newHarness(t)
	app := h.fakeApp()
	type result struct {
		Walls []map[string]any `json:"walls"`
	}
	got := make(chan result, 1)
	errc := make(chan error, 1)
	go func() {
		var r result
		errc <- h.call("app.state", map[string]any{}, &r)
		got <- r
	}()
	req := app.request()
	if req.Method != "app.state" {
		t.Fatalf("method %q", req.Method)
	}
	var id string
	if json.Unmarshal(req.ID, &id) != nil || id == "" {
		t.Fatalf("request id %s: want a string", req.ID)
	}
	app.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"walls": []any{map[string]any{"id": "main"}}}})
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if r := <-got; len(r.Walls) != 1 || r.Walls[0]["id"] != "main" {
		t.Fatalf("result %+v", r)
	}

	// Params pass through; the app's error comes back with its code.
	errc2 := make(chan error, 1)
	// An agent may call it too (the agent tree's policy is about agents.*);
	// its caller is not passed to the app.
	go func() {
		errc2 <- h.call("app.open", map[string]any{"agent": "L/zzzzzz", "mode": "focus", "caller": "L/nobody"}, nil)
	}()
	req = app.request()
	if strings.Contains(string(req.Params), "caller") {
		t.Fatalf("caller forwarded: %s", req.Params)
	}
	var p struct{ Agent, Mode string }
	json.Unmarshal(req.Params, &p)
	if req.Method != "app.open" || p.Agent != "L/zzzzzz" || p.Mode != "focus" {
		t.Fatalf("request %+v", req)
	}
	json.Unmarshal(req.ID, &id)
	app.send(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32000, "message": "no agent L/zzzzzz", "data": map[string]any{"code": "not_found"}}})
	if err := <-errc2; asWire(err) == nil || asWire(err).Code != wire.CodeNotFound {
		t.Fatalf("error %v", err)
	}
}

func TestAppControlWithoutApp(t *testing.T) {
	h := newHarness(t)
	for _, m := range []string{"app.state", "app.open", "app.wall.set", "app.desk"} {
		err := h.call(m, map[string]any{}, nil)
		if we := asWire(err); we == nil || we.Code != wire.CodeUnavailable {
			t.Fatalf("%s: %v", m, err)
		}
	}
}

func TestAppControlDisconnectMidRequest(t *testing.T) {
	h := newHarness(t)
	app := h.fakeApp()
	done := callAsync(h, "app.state", nil)
	app.request()
	app.conn.Close()
	select {
	case err := <-done:
		if we := asWire(err); we == nil || we.Code != wire.CodeUnavailable {
			t.Fatalf("error %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the call did not end with the app's connection")
	}
	// Gone: the next call fails at once.
	waitFor(t, func() bool {
		we := asWire(h.call("app.state", nil, nil))
		return we != nil && we.Code == wire.CodeUnavailable
	})
}

func TestAppControlTimeout(t *testing.T) {
	h := newHarness(t)
	h.srv.app.timeout = 200 * time.Millisecond
	app := h.fakeApp()
	start := time.Now()
	done := callAsync(h, "app.desk", map[string]any{"action": "list"})
	app.request() // never answered
	err := <-done
	if we := asWire(err); we == nil || we.Code != "timeout" {
		t.Fatalf("error %v", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("took %v", d)
	}
	// A late answer is dropped, not misdelivered, and the app stays registered.
	app.send(map[string]any{"jsonrpc": "2.0", "id": "app-1", "result": map[string]any{}})
	done = callAsync(h, "app.desk", map[string]any{"action": "list"})
	req := app.request()
	var id string
	json.Unmarshal(req.ID, &id)
	app.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": []any{}})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestAppControlLatestRegistrationWins(t *testing.T) {
	h := newHarness(t)
	first := h.fakeApp()
	second := h.fakeApp()
	done := callAsync(h, "app.state", nil)
	req := second.request()
	var id string
	json.Unmarshal(req.ID, &id)
	// The first app can't answer the second's request.
	first.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"from": "first"}})
	second.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"from": "second"}})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// The first app's connection still works as a plain client.
	first.send(map[string]any{"jsonrpc": "2.0", "id": 7, "method": "agents.list"})
	var res wire.Response
	first.read(&res)
	if string(res.ID) != "7" || res.Error != nil {
		t.Fatalf("agents.list on the old app: %+v", res)
	}
	// Closing the old app doesn't unregister the new one.
	first.conn.Close()
	time.Sleep(100 * time.Millisecond)
	done = callAsync(h, "app.state", nil)
	req = second.request()
	json.Unmarshal(req.ID, &id)
	second.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{}})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
