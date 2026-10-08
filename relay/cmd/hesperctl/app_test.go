package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// fakeHesperApp registers with hesperd as Hesper.app and answers app.*
// requests with answer (nil: echo the params); it records them.
type fakeHesperApp struct {
	mu     sync.Mutex
	got    []wire.Request
	answer func(wire.Request) (any, *wire.RPCError)
	conn   net.Conn
}

func startFakeApp(t *testing.T, sock string, answer func(wire.Request) (any, *wire.RPCError)) *fakeHesperApp {
	t.Helper()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	a := &fakeHesperApp{answer: answer, conn: conn}
	t.Cleanup(func() { conn.Close() })
	conn.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"app.register"}` + "\n"))
	r := bufio.NewReader(conn)
	if _, err := wire.ReadLine(r); err != nil { // the register reply
		t.Fatal(err)
	}
	go func() {
		for {
			line, err := wire.ReadLine(r)
			if err != nil {
				return
			}
			var req wire.Request
			json.Unmarshal(line, &req)
			a.mu.Lock()
			a.got = append(a.got, req)
			a.mu.Unlock()
			res := wire.Response{JSONRPC: "2.0", ID: req.ID}
			var result any = req.Params
			var rerr *wire.RPCError
			if a.answer != nil {
				result, rerr = a.answer(req)
			}
			if rerr != nil {
				res.Error = rerr
			} else {
				res.Result, _ = json.Marshal(result)
			}
			out, _ := json.Marshal(res)
			conn.Write(append(out, '\n'))
		}
	}()
	return a
}

func (a *fakeHesperApp) last(t *testing.T) (string, map[string]any) {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.got) == 0 {
		t.Fatal("the app got no request")
	}
	req := a.got[len(a.got)-1]
	var p map[string]any
	json.Unmarshal(req.Params, &p)
	return req.Method, p
}

func noLaunching(t *testing.T) {
	saved := launchApp
	launchApp = func(context.Context, bool) error { return errors.New("tests never open Hesper.app") }
	t.Cleanup(func() { launchApp = saved })
}

func TestAppCommandsSendTheirParams(t *testing.T) {
	noLaunching(t)
	_, sock, _ := daemon(t)
	app := startFakeApp(t, sock, nil)
	ctx := context.Background()
	cases := []struct {
		args   []string
		method string
		params string
	}{
		{[]string{"open", "agent", "a7f3k2"}, "app.open", `{"agent":"a7f3k2","mode":"focus"}`},
		{[]string{"open", "agent", "a7f3k2", "--window"}, "app.open", `{"agent":"a7f3k2","mode":"window"}`},
		{[]string{"open", "--tab", "L/a7f3k2"}, "app.open", `{"agent":"L/a7f3k2","mode":"tab"}`},
		{[]string{"open", "agent", "x", "--select"}, "app.open", `{"agent":"x","mode":"wall"}`},
		{[]string{"open", "wall", "--scope", "project:acme-apps"}, "app.open", `{"mode":"wall","scope":"project:acme-apps"}`},
		{[]string{"open", "wall", "--wall", "home", "--new"}, "app.open", `{"mode":"wall","newWall":true,"wall":"home"}`},
		{[]string{"open", "new", "--project", "~/p", "--kind", "codex", "--branch", "fix", "fix", "the", "login"}, "app.open",
			`{"composer":{"branch":"fix","kind":"codex","project":"~/p","task":"fix the login"}}`},
		{[]string{"open", "new", "--task", "hi", "--worktree", "--machine", "M"}, "app.open", `{"composer":{"machine":"M","task":"hi","worktree":true}}`},
		{[]string{"open", "history", "login", "bug"}, "app.open", `{"history":{"query":"login bug"}}`},
		{[]string{"open", "inbox"}, "app.open", `{"inbox":true}`},
		{[]string{"wall", "set", "--arrangement", "columns", "--density", "dense", "--collapse", "a", "--collapse", "b,c", "--expand", "all", "--sidebar", "off", "--home"}, "app.wall.set",
			`{"arrangement":"columns","collapse":["a","b","c"],"density":"dense","expand":["all"],"home":true,"sidebar":false}`},
		{[]string{"wall", "set", "--wall", "2", "--scope", "needs-you", "--band-order", "p:a,p:b", "--grouping", "project", "--own-walls", "full"}, "app.wall.set",
			`{"bandOrder":["p:a","p:b"],"grouping":"project","ownWalls":"full","scope":"needs-you","wall":"2"}`},
		{[]string{"desk"}, "app.desk", `{"action":"list"}`},
		{[]string{"desk", "save", "Deep work"}, "app.desk", `{"action":"save","name":"Deep work"}`},
		{[]string{"desk", "switch", "Deep work"}, "app.desk", `{"action":"switch","name":"Deep work"}`},
		{[]string{"desk", "rename", "Deep work", "Focus"}, "app.desk", `{"action":"rename","name":"Deep work","newName":"Focus"}`},
		{[]string{"desk", "rm", "Focus"}, "app.desk", `{"action":"remove","name":"Focus"}`},
	}
	for _, c := range cases {
		args := append(c.args, "--json", "--daemon-socket", sock)
		var code int
		capture(t, func() { code = execute(ctx, args, io.Discard) })
		if code != exitOK {
			t.Errorf("%v: exit %d", c.args, code)
			continue
		}
		method, p := app.last(t)
		got, _ := json.Marshal(p)
		if method != c.method || string(got) != c.params {
			t.Errorf("%v: %s %s, want %s %s", c.args, method, got, c.method, c.params)
		}
	}
}

func TestAppCommandsPrintAndUsage(t *testing.T) {
	noLaunching(t)
	_, sock, _ := daemon(t)
	startFakeApp(t, sock, func(req wire.Request) (any, *wire.RPCError) {
		switch req.Method {
		case "app.state":
			return map[string]any{"focused": map[string]any{"wall": "main"}, "walls": []any{
				map[string]any{"id": "main", "isHome": true, "open": true, "scope": "all", "arrangement": "shelf", "grouping": "auto", "density": "normal",
					"mode": "focus", "focusedAgent": "L/a", "scopeName": "acme apps", "agents": []string{"L/a", "L/b", "M/c"}, "bands": []any{map[string]any{"key": "p:as", "title": "acme-apps", "collapsed": true, "agents": 2}, map[string]any{"key": "p:gh", "title": "ghosty", "agents": 1}}},
			}, "desks": []any{map[string]any{"id": "d1", "name": "Laptop only", "current": true, "automatic": true}}}, nil
		case "app.desk":
			return []any{map[string]any{"id": "d1", "name": "Laptop only", "current": true, "automatic": true, "thisDisplays": true, "displays": "Built-in", "walls": 1}}, nil
		case "app.open":
			return nil, &wire.RPCError{Code: -32000, Message: "no agent zz", Data: &wire.ErrorData{Code: wire.CodeNotFound}}
		}
		return map[string]any{}, nil
	})
	ctx := context.Background()
	out := capture(t, func() { execute(ctx, []string{"wall", "--daemon-socket", sock}, io.Discard) })

	for _, want := range []string{"main (home)", "normal   3 ", "focus L/a", "shelf", "p:as", "collapsed", "desk: Laptop only", "focused: main"} {
		if !strings.Contains(out, want) {
			t.Errorf("wall lacks %q:\n%s", want, out)
		}
	}
	out = capture(t, func() { execute(ctx, []string{"desk", "--daemon-socket", sock}, io.Discard) })
	if !strings.Contains(out, "Laptop only (automatic)") || !strings.Contains(out, "*") {
		t.Errorf("desk:\n%s", out)
	}
	cases := []struct {
		args []string
		exit int
	}{
		{[]string{"open", "agent", "zz"}, exitNotFound}, // the app's error code
		{[]string{"open"}, exitUsage},
		{[]string{"open", "agent"}, exitUsage},
		{[]string{"open", "wall", "--window"}, exitUsage},
		{[]string{"open", "agent", "a", "--window", "--tab"}, exitUsage},
		{[]string{"open", "new", "--task", "a", "b"}, exitUsage},
		{[]string{"wall", "set"}, exitUsage},
		{[]string{"wall", "set", "--sidebar", "maybe"}, exitUsage},
		{[]string{"wall", "show", "--arrangement", "grid"}, exitUsage},
		{[]string{"wall", "spin"}, exitUsage},
		{[]string{"desk", "save"}, exitUsage},
		{[]string{"desk", "rename", "a"}, exitUsage},
		{[]string{"desk", "fly", "a"}, exitUsage},
	}
	for _, c := range cases {
		code := execute(ctx, append(c.args, "--daemon-socket", sock), io.Discard)
		if code != c.exit {
			t.Errorf("%v: exit %d, want %d", c.args, code, c.exit)
		}
	}
}

func TestAppCommandsWithoutApp(t *testing.T) {
	_, sock, _ := daemon(t)
	ctx := context.Background()
	saved, savedWait := launchApp, appLaunchWait
	t.Cleanup(func() { launchApp, appLaunchWait = saved, savedWait })

	// --no-launch: exit 4 at once, nothing opened.
	launched := 0
	launchApp = func(context.Context, bool) error { launched++; return nil }
	if exit, code, _ := jsonError(t, "wall", "--no-launch", "--json", "--daemon-socket", sock); exit != exitUnavailable || code != wire.CodeUnavailable || launched != 0 {
		t.Errorf("--no-launch: exit %d code %q launched %d", exit, code, launched)
	}

	// Opening the app fails: exit 4.
	launchApp = func(context.Context, bool) error { return errors.New("no Hesper.app") }
	if exit, _, msg := jsonError(t, "desk", "--json", "--daemon-socket", sock); exit != exitUnavailable || !strings.Contains(msg, "could not be opened") {
		t.Errorf("launch fails: exit %d %q", exit, msg)
	}

	// The app never registers: exit 4 after the wait.
	appLaunchWait = 300 * time.Millisecond
	launchApp = func(context.Context, bool) error { return nil }
	if exit, _, msg := jsonError(t, "desk", "--json", "--daemon-socket", sock); exit != exitUnavailable || !strings.Contains(msg, "did not connect") {
		t.Errorf("never registers: exit %d %q", exit, msg)
	}

	// The app starts and registers a moment later: the call goes through.
	appLaunchWait = 5 * time.Second
	var background []bool
	launchApp = func(_ context.Context, bg bool) error {
		background = append(background, bg)
		go func() {
			time.Sleep(200 * time.Millisecond)
			startFakeApp(t, sock, nil)
		}()
		return nil
	}
	var code int
	capture(t, func() { code = execute(ctx, []string{"open", "inbox", "--json", "--daemon-socket", sock}, io.Discard) })
	if code != exitOK || len(background) != 1 || background[0] {
		t.Errorf("launched: exit %d, launches %v (open brings the app to the front)", code, background)
	}
}
