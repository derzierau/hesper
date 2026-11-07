package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/derzierau/hesper/relay/internal/agents"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// fakeDevices is the daemon's devices hook: a fixed list, and the
// devices.remove calls it got.
type fakeDevices struct {
	mu      sync.Mutex
	list    []wire.Device
	removed []wire.DeviceRemoveParams
}

func (f *fakeDevices) Call(_ context.Context, method string, params json.RawMessage) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if method == "devices.list" {
		return wire.DevicesList{Devices: f.list}, nil
	}
	var p wire.DeviceRemoveParams
	json.Unmarshal(params, &p)
	f.removed = append(f.removed, p)
	return wire.DeviceRemoval{Relay: wire.StepDone, Approval: wire.StepSkipped, Trust: wire.StepDone, Names: wire.StepDone}, nil
}

// devicesDaemon is a daemon whose devices methods are fake.
func devicesDaemon(t *testing.T, devices agents.DeviceRemoval) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "gfgt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "state", "hesperd.sock")
	reg, err := agents.Open(agents.Options{StateDir: filepath.Join(dir, "state"), ConfigDir: filepath.Join(dir, "config"), Socket: sock, Machine: "L",
		Env: os.Environ(), LoginShell: "/bin/sh", ClaudeConfig: filepath.Join(dir, "claude.json"), CodexHome: filepath.Join(dir, "codex"),
		WorktreeRoot: filepath.Join(dir, "wt"), Logf: t.Logf, Removal: devices})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := agents.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := agents.NewServer(reg)
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close(); reg.Close() })
	return sock
}

func TestForgetResolvesNamesAndCallsRemove(t *testing.T) {
	fake := &fakeDevices{list: []wire.Device{
		{ID: "self-h", Name: "This Mac", Short: "me", Role: "host", Kind: wire.DeviceMac, Self: true},
		{ID: "dev-a", Name: "Workbook", Short: "workbook", Role: "host", Kind: wire.DeviceMac},
		{ID: "dev-b", Name: "Workbook", Role: "controller", Kind: wire.DeviceMac},
		{ID: "dev-p", Name: "Phone", Role: "controller", Kind: wire.DeviceIPhone},
	}}
	sock := devicesDaemon(t, fake)
	run := func(args ...string) (string, int) {
		var code int
		var stderr bytes.Buffer
		out := capture(t, func() { code = execute(context.Background(), append(args, "--daemon-socket", sock), &stderr) })
		return out, code
	}
	// a unique name, as JSON
	out, code := run("forget", "phone", "--json")
	var res wire.DeviceRemoval
	if code != exitOK || json.Unmarshal([]byte(out), &res) != nil || res.Relay != wire.StepDone || res.Approval != wire.StepSkipped {
		t.Fatalf("forget phone: exit %d\n%s", code, out)
	}
	// an id, --local-only, plain text
	if out, code := run("forget", "dev-b", "--local-only"); code != exitOK || !strings.Contains(out, "Removed Workbook") || !strings.Contains(out, "relay done") {
		t.Fatalf("forget dev-b: exit %d\n%s", code, out)
	}
	fake.mu.Lock()
	got := append([]wire.DeviceRemoveParams(nil), fake.removed...)
	fake.mu.Unlock()
	want := []wire.DeviceRemoveParams{{DeviceID: "dev-p"}, {DeviceID: "dev-b", LocalOnly: true}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("devices.remove calls %+v, want %+v", got, want)
	}
	// ambiguous: exit 2 with the candidates; nothing removed
	exit, errCode, message := jsonError(t, "forget", "Workbook", "--json", "--daemon-socket", sock)
	if exit != exitUsage || errCode != codeAmbiguous || !strings.Contains(message, "dev-a") || !strings.Contains(message, "dev-b") {
		t.Errorf("ambiguous: exit %d %s %q", exit, errCode, message)
	}
	if exit, errCode, _ := jsonError(t, "forget", "nobody", "--json", "--daemon-socket", sock); exit == exitOK || errCode != wire.CodeNotFound {
		t.Errorf("unknown: exit %d %s", exit, errCode)
	}
	if exit, errCode, _ := jsonError(t, "forget", "--json", "--daemon-socket", sock); exit != exitUsage || errCode != codeUsage {
		t.Errorf("no argument: exit %d %s", exit, errCode)
	}
	fake.mu.Lock()
	if len(fake.removed) != 2 {
		t.Errorf("removed %+v", fake.removed)
	}
	fake.mu.Unlock()
	// hesperctl reference lists it
	var stderr bytes.Buffer
	if out := capture(t, func() { execute(context.Background(), []string{"reference"}, &stderr) }); !strings.Contains(out, "forget NAME|ID [--local-only] [--json]") || !strings.Contains(out, "Remove a device from the relay and this Mac") {
		t.Errorf("reference does not list forget")
	}
}
