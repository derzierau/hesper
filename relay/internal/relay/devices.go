package relay

import (
	"context"
	"sort"

	"github.com/derzierau/hesper/relay/internal/identity"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

// SetDeviceAdmin lets controllers list and revoke their owner's devices
// over the hub ("devices", "revoke"; protocol.md, "Devices") and makes
// hello announce the feature. list returns the owner's devices from the
// repository. revoke commits the revocation of the owner's device id and
// ends its connections; it is called without the hub lock, as it calls
// back into Revoke.
func (h *Hub) SetDeviceAdmin(list func(ctx context.Context, owner string) ([]identity.Device, error), revoke func(ctx context.Context, owner, id string) error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.listDevices, h.revokeDevice = list, revoke
}

// handleDevices answers "devices" and "revoke". The checks run under the
// hub lock; the repository calls run after it is released.
func (h *Hub) handleDevices(c *Connection, m protocol.Message) error {
	h.mu.Lock()
	if err := h.current(c, m); err != nil {
		h.mu.Unlock()
		return err
	}
	list, revoke := h.listDevices, h.revokeDevice
	if c.device.Role == identity.Host {
		h.mu.Unlock()
		return protocol.Err("forbidden", "Hosts may only publish snapshots and results")
	}
	if list == nil || revoke == nil || !protocol.ValidID(m.ID) {
		h.mu.Unlock()
		return protocol.Err("invalid_request", "Invalid request envelope")
	}
	owner := c.device.Owner
	target, known := h.devices[m.DeviceID]
	h.mu.Unlock()
	respond := func(result any, err *protocol.Error) {
		reply := protocol.Message{Type: "result", ID: m.ID, Error: err}
		if err == nil {
			reply.Result = protocol.JSON(result)
		}
		c.peer.Send(reply)
	}
	ctx, cancel := context.WithTimeout(context.Background(), h.timeout)
	defer cancel()
	if m.Type == "devices" {
		all, err := list(ctx, owner)
		if err != nil {
			respond(nil, protocol.PublicError(err))
			return nil
		}
		devices := []protocol.Device{}
		h.mu.Lock()
		for _, d := range all {
			if d.Owner != owner || d.Revoked {
				continue
			}
			devices = append(devices, protocol.Device{ID: d.ID, Name: d.Name, Role: string(d.Role), Online: h.connections[d.ID] != nil})
		}
		h.mu.Unlock()
		sort.Slice(devices, func(i, j int) bool { return devices[i].ID < devices[j].ID })
		c.peer.Send(protocol.Message{Type: "devices", ID: m.ID, Devices: devices})
		return nil
	}
	switch {
	case !protocol.ValidID(m.DeviceID):
		respond(nil, protocol.Err("invalid_request", "deviceId is required"))
	case m.DeviceID == c.device.ID:
		respond(nil, protocol.Err("forbidden", "This is the device you are using; sign out instead"))
	case !known || target.Owner != owner || target.Revoked:
		// Another owner's device is indistinguishable from an unknown one.
		respond(nil, protocol.Err("not_found", "Device not found"))
	default:
		if err := revoke(ctx, owner, m.DeviceID); err != nil {
			respond(nil, protocol.PublicError(err))
		} else {
			respond(struct{}{}, nil)
		}
	}
	return nil
}
