package relay

import (
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/internal/identity"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

// The relay passes a request's device-key signature on unchanged and names
// the sending controller; hosts verify both (Part K).
func TestRequestsCarryTheControllersSignature(t *testing.T) {
	host := device("host-a", "a", identity.Host)
	controller := device("client", "a", identity.Controller)
	h := New([]identity.Device{host, controller}, time.Second)
	defer h.Close()
	_, hostPeer := attach(t, h, host)
	c, _ := attach(t, h, controller)
	m := requestMessage("r1", host.ID)
	m.Auth = &protocol.Auth{Device: "client", TS: 1791036424000, Nonce: "AAECAwQFBgcICQoLDA0ODw==", Sig: "MEUCIQ=="}
	if err := h.Handle(c, m); err != nil {
		t.Fatal(err)
	}
	got := next(t, hostPeer, "request")
	if got.Auth == nil || *got.Auth != *m.Auth || got.ControllerID != "client" || string(got.Params) != string(m.Params) {
		t.Fatalf("forwarded %+v", got)
	}
}
