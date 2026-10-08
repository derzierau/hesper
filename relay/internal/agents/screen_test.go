package agents

import (
	"strings"
	"testing"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

func TestScreen(t *testing.T) {
	h := newHarness(t)
	a := h.spawn(wire.SpawnParams{Task: "approve this"})
	h.waitState(a.ID, wire.StateApproval)
	var res wire.ScreenResult
	if err := h.call("agents.screen", wire.ScreenParams{ID: a.ID}, &res); err != nil {
		t.Fatal(err)
	}
	if res.Cols == 0 || strings.Count(res.Text, "\n") != res.Rows-1 || strings.Contains(res.Text, "\x1b") || !strings.Contains(res.Text, "git push") {
		t.Fatalf("screen %+v\nwant %q", res, h.screen(a.ID))
	}
	if err := h.call("agents.screen", wire.ScreenParams{ID: a.ID, Rows: 2}, &res); err != nil || strings.Count(res.Text, "\n") != 1 {
		t.Fatalf("rows 2: %v %q", err, res.Text)
	}
	for _, p := range []wire.ScreenParams{{ID: a.ID, Rows: -1}, {ID: a.ID, Scrollback: wire.MaxScreenScrollback + 1}, {}} {
		if err := h.call("agents.screen", p, nil); err == nil {
			t.Errorf("%+v: no error", p)
		}
	}
	err := h.call("agents.screen", wire.ScreenParams{ID: "L/nope00"}, nil)
	if we, ok := err.(*wire.Error); !ok || we.Code != wire.CodeNotFound {
		t.Fatalf("unknown agent: %v", err)
	}
}
