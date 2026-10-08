package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/derzierau/hesper/relay/pkg/protocol"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// capture runs f with os.Stdout going to a pipe and returns what it wrote.
func capture(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	done := make(chan []byte)
	go func() { data, _ := io.ReadAll(r); done <- data }()
	defer func() { os.Stdout = saved }()
	f()
	w.Close()
	return string(<-done)
}

// Help lists flags by running each command with -h: every command must
// parse its flags before it does anything.
func TestEveryCommandParsesFirst(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, c := range commands {
		if err := c.Run(ctx, newFlagSet(c.Name), []string{"-h"}); !errors.Is(err, flag.ErrHelp) {
			t.Errorf("%s -h: %v", c.Name, err)
		}
		if c.Summary == "" || c.Usage == "" || c.Group == "" {
			t.Errorf("%s: summary, usage and group are required", c.Name)
		}
	}
}

func TestHelp(t *testing.T) {
	ctx := context.Background()
	overview := capture(t, func() {
		if code := execute(ctx, nil, io.Discard); code != exitOK {
			t.Errorf("no arguments: exit %d", code)
		}
	})
	for _, want := range []string{"Agents:", "Relay and devices:", "Help:", "  send ", "  reference ", "Exit codes:"} {
		if !strings.Contains(overview, want) {
			t.Errorf("overview lacks %q:\n%s", want, overview)
		}
	}
	if strings.Index(overview, "Help:") < strings.Index(overview, "Relay and devices:") {
		t.Error("Help is not the last group")
	}
	for _, args := range [][]string{{"help", "send"}, {"send", "--help"}, {"send", "ID", "-h"}} {
		out := capture(t, func() {
			if code := execute(ctx, args, io.Discard); code != exitOK {
				t.Errorf("%v: exit %d", args, code)
			}
		})
		if !strings.Contains(out, "Usage: hesperctl send ID TEXT…") || !strings.Contains(out, "--no-submit") || !strings.Contains(out, "--daemon-socket") {
			t.Errorf("%v:\n%s", args, out)
		}
	}
	out := capture(t, func() { execute(ctx, []string{"help", "new", "--json"}, io.Discard) })
	var doc commandDoc
	if err := json.Unmarshal([]byte(out), &doc); err != nil || doc.Name != "new" || doc.Output != "Agent" || len(doc.Flags) == 0 {
		t.Errorf("help new --json: %v %+v", err, doc)
	}
	for _, fl := range doc.Flags {
		if fl.Name == "json" {
			t.Error("--json is listed per command")
		}
		if fl.Name == "project" && fl.Default != "the current directory" {
			t.Errorf("--project default %q", fl.Default)
		}
	}
	var stderr bytes.Buffer
	if code := execute(ctx, []string{"help", "nope"}, &stderr); code != exitUsage {
		t.Errorf("help nope: exit %d", code)
	}
	if code := execute(ctx, []string{"nope"}, &stderr); code != exitUsage || !strings.Contains(stderr.String(), `unknown command "nope"`) {
		t.Errorf("nope: exit %d, %s", code, stderr.String())
	}
}

func TestReference(t *testing.T) {
	ctx := context.Background()
	md := capture(t, func() {
		if code := execute(ctx, []string{"reference"}, io.Discard); code != exitOK {
			t.Errorf("exit %d", code)
		}
	})
	for _, c := range commands {
		if !strings.Contains(md, "\n#### "+c.Name+"\n") {
			t.Errorf("reference lacks %s", c.Name)
		}
	}
	for _, want := range []string{wire.StateApproval, wire.StateExited, `"attention": {`, "HESPER_AGENT_ID", "| 3 | not_found |", "| 7 | exists |", "`--no-submit`"} {
		if !strings.Contains(md, want) {
			t.Errorf("reference lacks %q", want)
		}
	}
	out := capture(t, func() { execute(ctx, []string{"reference", "--json"}, io.Discard) })
	var ref struct {
		Agent     wire.Agent   `json:"agent"`
		ExitCodes []any        `json:"exitCodes"`
		Commands  []commandDoc `json:"commands"`
	}
	if err := json.Unmarshal([]byte(out), &ref); err != nil || len(ref.Commands) != len(commands) || len(ref.ExitCodes) != 8 || ref.Agent.ID == "" {
		t.Fatalf("reference --json: %v, %d commands", err, len(ref.Commands))
	}
}

func TestExitCodes(t *testing.T) {
	cases := []struct {
		err  error
		code int
		name string
	}{
		{nil, exitOK, ""},
		{errors.New("boom"), exitError, codeError},
		{usagef("usage: x"), exitUsage, codeUsage},
		{failf(codeAmbiguous, "two"), exitUsage, codeAmbiguous},
		{&wire.Error{Code: wire.CodeNotFound}, exitNotFound, wire.CodeNotFound},
		{fmt.Errorf("x: %w", &wire.Error{Code: wire.CodeNotFound}), exitNotFound, wire.CodeNotFound},
		{&wire.Error{Code: wire.CodeInvalid}, exitError, wire.CodeInvalid},
		{&wire.Error{Code: wire.CodeUnavailable}, exitUnavailable, wire.CodeUnavailable},
		{&wire.Error{Code: wire.CodeOffline}, exitUnavailable, wire.CodeOffline},
		{&wire.Error{Code: wire.CodeRemote}, exitUnavailable, wire.CodeRemote},
		{fmt.Errorf("%w (sock): refused", errNoDaemon), exitUnavailable, wire.CodeUnavailable},
		{&wire.Error{Code: wire.CodeForbidden}, exitForbidden, wire.CodeForbidden},
		{protocol.Err("unauthorized", "no"), exitForbidden, "unauthorized"},
		{failf(codeTimeout, "waited"), exitTimeout, codeTimeout},
		{context.DeadlineExceeded, exitTimeout, codeTimeout},
		{&wire.Error{Code: wire.CodeExists}, exitExists, wire.CodeExists},
		{&wire.Error{Code: wire.CodeLive}, exitExists, wire.CodeLive},
	}
	for _, c := range cases {
		if got := exitCode(c.err); got != c.code {
			t.Errorf("%v: exit %d, want %d", c.err, got, c.code)
		}
		if c.err != nil && errorCode(c.err) != c.name {
			t.Errorf("%v: code %q, want %q", c.err, errorCode(c.err), c.name)
		}
	}
}

// jsonError runs args and decodes the error it printed.
func jsonError(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stderr bytes.Buffer
	code := execute(context.Background(), args, &stderr)
	var e struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if err := json.Unmarshal(stderr.Bytes(), &e); err != nil {
		t.Fatalf("%v: stderr %q: %v", args, stderr.String(), err)
	}
	return code, e.Error.Code, e.Error.Message
}

func TestJSONErrors(t *testing.T) {
	_, sock, _ := daemon(t)
	none := filepath.Join(t.TempDir(), "none.sock")
	cases := []struct {
		args []string
		exit int
		code string
	}{
		{[]string{"stop", "--json", "--daemon-socket", sock, "nope00"}, exitNotFound, wire.CodeNotFound},
		{[]string{"ls", "--daemon-socket", none, "--json"}, exitUnavailable, wire.CodeUnavailable},
		{[]string{"ls", "--json", "--bogus"}, exitUsage, codeUsage},
		{[]string{"send", "--json", "--daemon-socket", sock}, exitUsage, codeUsage},
		{[]string{"nope", "--json"}, exitUsage, codeUsage},
		// Relay commands take --json too.
		{[]string{"revoke", "--json"}, exitUsage, codeUsage},
	}
	for _, c := range cases {
		exit, code, message := jsonError(t, c.args...)
		if exit != c.exit || code != c.code || message == "" {
			t.Errorf("%v: exit %d code %q %q", c.args, exit, code, message)
		}
	}
	// Without --json the message is plain text.
	var stderr bytes.Buffer
	execute(context.Background(), []string{"ls", "--daemon-socket", none}, &stderr)
	if !strings.HasPrefix(stderr.String(), "hesperd is not running") {
		t.Errorf("plain error %q", stderr.String())
	}
}

func TestSelf(t *testing.T) {
	reg, sock, project := daemon(t)
	ctx := context.Background()
	if err := run(ctx, []string{"new", "--daemon-socket", sock, "--project", project, "--name", "me", "hi"}); err != nil {
		t.Fatal(err)
	}
	a := reg.List()[0]
	_, local, _ := strings.Cut(a.ID, "/")
	self := func(env ...string) (int, string) {
		t.Setenv("HESPER_AGENT_ID", env[0])
		t.Setenv("HESPER_MACHINE", env[1])
		var code int
		out := capture(t, func() { code = execute(ctx, []string{"self", "--json", "--daemon-socket", sock}, io.Discard) })
		return code, out
	}
	for _, env := range [][]string{{a.ID, ""}, {local, a.Machine}, {local, ""}} {
		code, out := self(env...)
		var got wire.Agent
		if code != exitOK || json.Unmarshal([]byte(out), &got) != nil || got.ID != a.ID {
			t.Errorf("%v: exit %d %s", env, code, out)
		}
	}
	for _, env := range [][]string{{"", ""}, {"L/nope00", ""}, {local, "elsewhere"}} {
		if code, _ := self(env...); code != exitNotFound {
			t.Errorf("%v: exit %d", env, code)
		}
	}
	t.Setenv("HESPER_AGENT_ID", "")
	exit, code, message := jsonError(t, "self", "--json", "--daemon-socket", sock)
	if exit != exitNotFound || code != wire.CodeNotFound || !strings.Contains(message, "not inside a Hesper agent") {
		t.Errorf("outside an agent: %d %s %s", exit, code, message)
	}
}
