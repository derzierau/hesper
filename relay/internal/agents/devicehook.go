package agents

import (
	"context"
	"encoding/json"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Devices are the host role's pairing offers (internal/host, offers.go),
// hooked in here: devices.offer and devices.offerCancel on the local
// socket (a person's only, tree.go) and the devices.paired notification.
// Nil (tests): no devices methods.
type Devices interface {
	// Call runs a devices method (handled false: not one).
	Call(method string, params json.RawMessage) (result any, err error, handled bool)
	// Watch sends the devices notifications of an agents.subscribe
	// connection until done closes or send fails.
	Watch(done <-chan struct{}, send func(method string, params any) error)
}

// DeviceRemoval (internal/host removal.go) serves devices.list and
// devices.remove: every device of the owner, and removing one from the
// relay and this Mac. Only a person calls them (treePerson). Nil: the
// methods are unavailable.
type DeviceRemoval interface {
	Call(ctx context.Context, method string, params json.RawMessage) (any, error)
}

// watchDevices sends the pairing notifications on a subscribed control
// connection (server.go's subscribe) until it closes.
func (c *ctrl) watchDevices() {
	d := c.s.reg.opt.Devices
	if d == nil {
		return
	}
	d.Watch(c.done, func(method string, params any) error {
		data, err := json.Marshal(params)
		if err != nil {
			return err
		}
		return c.write(wire.Notification{JSONRPC: "2.0", Method: method, Params: data})
	})
}
