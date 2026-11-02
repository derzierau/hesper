package agents

import (
	"errors"
	"testing"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Pairing offers are a person's: an agent may not open or end one.
// Without the host role (Devices nil) a person gets unavailable.
func TestDeviceOffersArePersonOnly(t *testing.T) {
	h := newHarness(t)
	agent := h.spawn(wire.SpawnParams{Task: "small job"})
	for _, method := range []string{"devices.offer", "devices.offerCancel"} {
		forbidden(t, method+" by an agent", h.callAs(agent.ID, method, map[string]any{}, nil))
		var we *wire.Error
		if err := h.call(method, map[string]any{}, nil); !errors.As(err, &we) || we.Code != wire.CodeUnavailable {
			t.Fatalf("%s without the host role: %v", method, err)
		}
	}
}
