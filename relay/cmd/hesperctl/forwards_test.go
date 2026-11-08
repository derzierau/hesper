package main

import (
	"strings"
	"testing"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Without the controller role there are no forwards to list and none to
// add; the arguments are checked first. (Forwards themselves are tested
// end to end in internal/transport/remote_forward_test.go.)
func TestForwardCommands(t *testing.T) {
	e := fullDaemon(t)
	var list []wire.Forward
	e.okJSON(t, &list, "forwards", "ls")
	if list == nil || len(list) != 0 {
		t.Fatalf("ls: %+v", list)
	}
	if out := e.ok(t, "forwards", "ls"); !strings.Contains(out, "LOCAL") {
		t.Errorf("ls:\n%s", out)
	}
	e.fails(t, exitUnavailable, wire.CodeUnavailable, "forwards", "add", "dev:3000")
	for _, bad := range []string{"dev", ":3000", "dev:x", "dev:0", "dev:70000"} {
		e.fails(t, exitUsage, codeUsage, "forwards", "add", bad)
	}
	e.fails(t, exitUsage, codeUsage, "forwards", "add", "dev:3000", "--local", "-1")
	e.fails(t, exitUsage, codeUsage, "forwards", "rm")
	e.fails(t, exitUnavailable, wire.CodeUnavailable, "forwards", "rm", "3000")
}
