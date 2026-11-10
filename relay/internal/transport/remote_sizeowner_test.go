package transport_test

import (
	"testing"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Who owns a terminal's size, end to end between two daemons: L's owner
// attach to an agent on M is published by M with L's device and name,
// and L's own clients see it as absent (this Mac owns it); M's own owner
// attach is sizeOwnerHost on both; when M lets go, the size falls back to
// L's attach, still open, and L's clients get that change.
func TestRemoteSizeOwner(t *testing.T) {
	w := newWorld(t, worldOptions{shell: true})
	a := spawnShell(t, w)
	onM := localOn(a.ID, "M")
	owners := func(x wire.Agent) [3]any { return [3]any{x.SizeOwner, x.SizeOwnerName, x.SizeOwnerHost} }

	// L owns it.
	rw := attach(t, w.L, wire.AttachRequest{Attach: a.ID, Mode: wire.ModeRW, Owner: true, Cols: 100, Rows: 30})
	readUntil(t, rw, "$")
	w.M.waitAgent(t, onM, func(x wire.Agent) bool { return owners(x) == [3]any{w.L.ctlID, "L", false} })
	w.L.waitAgent(t, a.ID, func(x wire.Agent) bool {
		return x.Size == wire.Size{Cols: 100, Rows: 30} && owners(x) == [3]any{"", "", false}
	})

	// M takes it back: the agent's machine owns it, on both.
	local := attach(t, w.M, wire.AttachRequest{Attach: onM, Mode: wire.ModeRW, Owner: true, Cols: 80, Rows: 24})
	w.M.waitAgent(t, onM, func(x wire.Agent) bool { return owners(x) == [3]any{"", "", true} })
	w.L.waitAgent(t, a.ID, func(x wire.Agent) bool {
		return x.Size == wire.Size{Cols: 80, Rows: 24} && owners(x) == [3]any{"", "", true}
	})

	// M lets go: back to L's attach, at L's grid.
	local.Close()
	w.M.waitAgent(t, onM, func(x wire.Agent) bool { return owners(x) == [3]any{w.L.ctlID, "L", false} })
	w.L.waitAgent(t, a.ID, func(x wire.Agent) bool {
		return x.Size == wire.Size{Cols: 100, Rows: 30} && owners(x) == [3]any{"", "", false}
	})

	// L lets go: nobody, on both.
	rw.Close()
	w.M.waitAgent(t, onM, func(x wire.Agent) bool { return owners(x) == [3]any{"", "", false} })
	w.L.waitAgent(t, a.ID, func(x wire.Agent) bool { return owners(x) == [3]any{"", "", false} })
}
