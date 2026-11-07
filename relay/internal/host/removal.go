package host

// Removing a device everywhere (devices.list and devices.remove on the
// local socket, a person only): the owner's devices as the relay lists
// them, merged with what this Mac keeps about them (controllers.json,
// trusted-hosts.json, machines.json), and one removal that revokes a
// device on the relay and then forgets it here.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/derzierau/hesper/relay/pkg/e2e"
	"github.com/derzierau/hesper/relay/pkg/protocol"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// RelayDevices is this Mac's controller connection to the relay
// (remote.Fleet). Devices and Revoke may be called only when Supports
// (protocol.FeatureDevices): an older relay closes the connection on them.
type RelayDevices interface {
	Connected() bool
	Supports(feature string) bool
	Devices(ctx context.Context) ([]protocol.Device, error)
	Revoke(ctx context.Context, id string) error
}

// DevicesService serves devices.list and devices.remove.
type DevicesService struct {
	Store        Store  // controllers.json and the audit log
	TrustPath    string // trusted-hosts.json
	MachinesPath string // ~/.config/hesper/machines.json
	// SelfHost and SelfController are this Mac's own relay device ids
	// (host.credentials.json, controller.credentials.json): never removed.
	SelfHost, SelfController string
	// Relay is nil without the controller role.
	Relay RelayDevices
}

// Call runs devices.list or devices.remove.
func (s *DevicesService) Call(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "devices.list":
		list, err := s.List(ctx)
		if err != nil {
			return nil, err
		}
		return wire.DevicesList{Devices: list}, nil
	case "devices.remove":
		var p wire.DeviceRemoveParams
		if len(params) > 0 && string(params) != "null" {
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, wire.Errorf(wire.CodeInvalid, "devices.remove: %v", err)
			}
		}
		return s.Remove(ctx, p.DeviceID, p.LocalOnly)
	}
	return nil, wire.Errorf(wire.CodeNotFound, "no method %s", method)
}

// List is every device the relay or this Mac knows. Without a relay
// that answers (no controller role, offline, too old) it is this Mac's
// files alone, every entry offline.
func (s *DevicesService) List(ctx context.Context) ([]wire.Device, error) {
	controllers, trust, names, err := s.local()
	if err != nil {
		return nil, err
	}
	var relay []protocol.Device
	known := false
	if s.Relay != nil && s.Relay.Connected() && s.Relay.Supports(protocol.FeatureDevices) {
		if list, err := s.Relay.Devices(ctx); err == nil {
			relay, known = list, true
		}
	}
	return MergeDevices(relay, known, controllers, trust.Hosts, names, s.SelfHost, s.SelfController), nil
}

// local reads this Mac's three files.
func (s *DevicesService) local() ([]Controller, e2e.Trust, map[string]string, error) {
	list, _, err := s.Store.LoadControllers()
	if err != nil {
		return nil, e2e.Trust{}, nil, err
	}
	trust, err := e2e.LoadTrust(s.TrustPath)
	if err != nil {
		return nil, e2e.Trust{}, nil, err
	}
	names, err := loadMachineNames(s.MachinesPath)
	if err != nil {
		return nil, e2e.Trust{}, nil, err
	}
	return list.Controllers, trust, names, nil
}

// MergeDevices is the union by id of the relay's list (only when
// relayKnown), the controllers approved here, the hosts pinned here and
// the short names of machines.json. A device is an iPhone exactly when
// the relay has an APNs registration for it (push). Sorted: this Mac's
// own ids first (host, then controller), then Macs by short name or
// name, then iPhones by name.
func MergeDevices(relay []protocol.Device, relayKnown bool, controllers []Controller, trust map[string]e2e.TrustedHost,
	names map[string]string, selfHost, selfController string) []wire.Device {
	byID := map[string]*wire.Device{}
	get := func(id, role string) *wire.Device {
		d := byID[id]
		if d == nil {
			d = &wire.Device{ID: id, Role: role, Kind: wire.DeviceMac}
			byID[id] = d
		}
		return d
	}
	if relayKnown {
		for _, r := range relay {
			d := get(r.ID, r.Role)
			d.Name, d.Online, d.Relay = r.Name, r.Online, true
			if r.Push {
				d.Kind = wire.DeviceIPhone
			}
		}
	}
	for _, c := range controllers {
		d := get(c.Device, "controller")
		d.ApprovedHere = true
		if d.Name == "" {
			d.Name = c.Name
		}
	}
	for id, h := range trust {
		d := get(id, "host")
		d.TrustedHere = true
		if d.Name == "" {
			d.Name = h.Name
		}
	}
	for id, short := range names {
		d := get(id, "host")
		d.Short = short
		if d.Name == "" {
			d.Name = short
		}
	}
	list := make([]wire.Device, 0, len(byID))
	for _, d := range byID {
		d.Self = d.ID != "" && (d.ID == selfHost || d.ID == selfController)
		list = append(list, *d)
	}
	rank := func(d wire.Device) int {
		switch {
		case d.Self && d.Role == "host":
			return 0
		case d.Self:
			return 1
		case d.Kind == wire.DeviceMac:
			return 2
		}
		return 3
	}
	label := func(d wire.Device) string {
		if d.Kind == wire.DeviceMac && d.Short != "" {
			return strings.ToLower(d.Short)
		}
		return strings.ToLower(d.Name)
	}
	slices.SortFunc(list, func(a, b wire.Device) int {
		if ra, rb := rank(a), rank(b); ra != rb {
			return ra - rb
		}
		if c := strings.Compare(label(a), label(b)); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return list
}

// Remove removes device id everywhere: first on the relay (unless
// localOnly), then its approval on this host, its pinned key and its
// short name. The relay must take part: unreachable is unavailable, too
// old is unsupported, and a refusal is returned as it is, each before
// anything changes here. A device the relay does not list (already
// revoked, or never known there) skips the relay step. Removing a host
// also revokes the offline controller records of the same name (a Mac's
// second identity), reported in SignedOut.
func (s *DevicesService) Remove(ctx context.Context, id string, localOnly bool) (wire.DeviceRemoval, error) {
	res := wire.DeviceRemoval{Relay: wire.StepSkipped, Approval: wire.StepSkipped, Trust: wire.StepSkipped, Names: wire.StepSkipped}
	if !deviceID.MatchString(id) {
		return res, wire.Errorf(wire.CodeInvalid, "devices.remove needs a deviceId")
	}
	if id == s.SelfHost || id == s.SelfController {
		return res, wire.Errorf(wire.CodeForbidden, "%s is this Mac; it cannot remove itself", id)
	}
	var listed []protocol.Device
	if !localOnly {
		if s.Relay == nil || !s.Relay.Connected() {
			return res, wire.Errorf(wire.CodeUnavailable, "the relay is unreachable")
		}
		if !s.Relay.Supports(protocol.FeatureDevices) {
			return res, wire.Errorf(wire.CodeUnsupported, "The relay needs an update to remove devices")
		}
		list, err := s.Relay.Devices(ctx)
		if err != nil {
			return res, relayError(err)
		}
		listed = list
	}
	controllers, trust, names, err := s.local()
	if err != nil {
		return res, err
	}
	approved := func(id string) bool {
		return slices.ContainsFunc(controllers, func(c Controller) bool { return c.Device == id })
	}
	_, pinned := trust.Hosts[id]
	_, named := names[id]
	i := slices.IndexFunc(listed, func(d protocol.Device) bool { return d.ID == id })
	if i < 0 && !approved(id) && !pinned && !named {
		return res, wire.Errorf(wire.CodeNotFound, "no device %s", id)
	}
	name := names[id]
	if i >= 0 {
		target := listed[i]
		name = target.Name
		// not_found right after the relay listed it: revoked meanwhile.
		if err := s.Relay.Revoke(ctx, id); err != nil && !relayCode(err, wire.CodeNotFound) {
			return res, relayError(err)
		}
		res.Relay = wire.StepDone
		if target.Role == "host" {
			for _, d := range listed {
				if d.Role != "controller" || d.Online || !strings.EqualFold(d.Name, target.Name) || d.ID == s.SelfController {
					continue
				}
				if err := s.Relay.Revoke(ctx, d.ID); err != nil && !relayCode(err, wire.CodeNotFound) {
					continue // best effort: the host itself is gone
				}
				res.SignedOut = append(res.SignedOut, d.ID)
				if approved(d.ID) {
					s.Store.Revoke(d.ID)
				}
			}
		}
	}
	if approved(id) {
		_, err := s.Store.Revoke(id)
		res.Approval = step(err)
	}
	if pinned {
		res.Trust = step(e2e.UpdateTrust(s.TrustPath, func(t *e2e.Trust) (bool, error) {
			_, ok := t.Hosts[id]
			delete(t.Hosts, id)
			return ok, nil
		}))
	}
	if named {
		res.Names = step(dropMachineName(s.MachinesPath, id))
	}
	ok := !slices.ContainsFunc([]string{res.Approval, res.Trust, res.Names}, func(v string) bool { return v != wire.StepDone && v != wire.StepSkipped })
	detail := fmt.Sprintf("relay %s, approval %s, trust %s, names %s", res.Relay, res.Approval, res.Trust, res.Names)
	if len(res.SignedOut) > 0 {
		detail += ", signed out " + strings.Join(res.SignedOut, " ")
	}
	s.Store.Audit("device.removed", map[string]any{"device": id, "name": name, "ok": ok, "detail": detail})
	return res, nil
}

// step is a step's outcome: done or the error's text.
func step(err error) string {
	if err != nil {
		return err.Error()
	}
	return wire.StepDone
}

// relayCode reports whether err is the relay's error code.
func relayCode(err error, code string) bool {
	var pe *protocol.Error
	return errors.As(err, &pe) && pe.Code == code
}

// relayError keeps the relay's code (not_found, forbidden, timeout, …);
// anything else means the relay could not be asked.
func relayError(err error) error {
	var pe *protocol.Error
	if errors.As(err, &pe) {
		return wire.Errorf(pe.Code, "relay: %s", pe.Message)
	}
	return wire.Errorf(wire.CodeUnavailable, "relay: %v", err)
}

// machinesFile is machines.json as far as removal reads it; writes keep
// every other field (glyph, color, …) as it is.
type machinesFile struct {
	Machines map[string]struct {
		Short string `json:"short"`
	} `json:"machines"`
}

// loadMachineNames is machines.json's short names by relay device id; a
// missing file has none.
func loadMachineNames(path string) (map[string]string, error) {
	names := map[string]string{}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return names, nil
	}
	if err != nil {
		return nil, err
	}
	var f machinesFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for id, m := range f.Machines {
		names[id] = m.Short
	}
	return names, nil
}

// dropMachineName removes id from machines.json, keeping everything else.
func dropMachineName(path, id string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	var machines map[string]json.RawMessage
	if err := json.Unmarshal(top["machines"], &machines); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if _, ok := machines[id]; !ok {
		return nil
	}
	delete(machines, id)
	top["machines"], _ = json.Marshal(machines)
	out, _ := json.MarshalIndent(top, "", "  ")
	if err := writeAtomic(path, append(out, '\n')); err != nil {
		return err
	}
	return os.Chmod(path, 0o644) // as hesperctl machines --save writes it
}
