package remote

import (
	"testing"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// A host publishes the controller that owns a terminal's size. When that
// controller is this Mac (its own tile owns a remote agent's size), the
// owner is local in this Mac's naming, so its tiles never turn into fit
// views of their own attach; another device's ownership passes through.
func TestNamedSizeOwner(t *testing.T) {
	f, m := &Fleet{device: "mac-1"}, &machine{short: "M"}
	own := f.named(m, wire.Agent{ID: "a7f3k2", SizeOwner: "mac-1", SizeOwnerName: "Laptop"})
	if own.ID != "M/a7f3k2" || own.SizeOwner != "" || own.SizeOwnerName != "" {
		t.Fatalf("own attach %+v", own)
	}
	phone := f.named(m, wire.Agent{ID: "a7f3k2", SizeOwner: "phone-1", SizeOwnerName: "iPhone"})
	if phone.SizeOwner != "phone-1" || phone.SizeOwnerName != "iPhone" {
		t.Fatalf("phone %+v", phone)
	}
	// The host owning it passes through: there, the agent's machine.
	if host := f.named(m, wire.Agent{ID: "x", SizeOwnerHost: true}); !host.SizeOwnerHost || host.SizeOwner != "" {
		t.Fatalf("host %+v", host)
	}
	if none := (&Fleet{}).named(m, wire.Agent{ID: "x"}); none.SizeOwner != "" || none.SizeOwnerHost {
		t.Fatalf("none %+v", none)
	}
}
