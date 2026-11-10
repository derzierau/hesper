package remote

import (
	"context"
	"time"

	"github.com/derzierau/hesper/relay/pkg/client"
	"github.com/derzierau/hesper/relay/pkg/protocol"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// The controller connection's devices and revoke (host.RelayDevices, for
// devices.list and devices.remove).

// helloWait bounds the wait for the relay's hello on a fresh connection.
const helloWait = 5 * time.Second

func (f *Fleet) controller() *client.Controller {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.c
}

// Connected reports whether the relay connection is up.
func (f *Fleet) Connected() bool { return f.controller() != nil }

// Supports reports whether the relay's hello announced feature (waiting
// for the hello of a connection that just opened).
func (f *Fleet) Supports(feature string) bool {
	c := f.controller()
	if c == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), helloWait)
	defer cancel()
	return c.WaitHello(ctx) == nil && c.Supports(feature)
}

// Devices is the relay's list of the owner's devices.
func (f *Fleet) Devices(ctx context.Context) ([]protocol.Device, error) {
	c := f.controller()
	if c == nil {
		return nil, wire.Errorf(wire.CodeUnavailable, "the relay is unreachable")
	}
	return c.Devices(ctx)
}

// Revoke revokes one of the owner's devices on the relay.
func (f *Fleet) Revoke(ctx context.Context, id string) error {
	c := f.controller()
	if c == nil {
		return wire.Errorf(wire.CodeUnavailable, "the relay is unreachable")
	}
	return c.Revoke(ctx, id)
}
