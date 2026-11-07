package main

// `hesperctl forget NAME|ID`: remove a device from the relay and this Mac
// in one step, through the daemon (devices.list, devices.remove).
// `devices-local --revoke` and `trust --reset` stay the local-only tools.

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

func forgetCommand(ctx context.Context, f *flag.FlagSet, args []string) error {
	localOnly := f.Bool("local-only", false, "Leave the relay alone (for a relay too old to remove devices): forget the device on this Mac only")
	asJSON := jsonFlag(f)
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) != 1 {
			return usagef("usage: hesperctl forget NAME|ID [--local-only]")
		}
		var list wire.DevicesList
		if err := c.Call(ctx, "devices.list", nil, &list); err != nil {
			return err
		}
		d, err := resolveDevice(list.Devices, positional[0])
		if err != nil {
			return err
		}
		var res wire.DeviceRemoval
		if err := c.Call(ctx, "devices.remove", wire.DeviceRemoveParams{DeviceID: d.ID, LocalOnly: *localOnly}, &res); err != nil {
			return err
		}
		if *asJSON {
			return output(res)
		}
		fmt.Printf("Removed %s (%s): relay %s, approval %s, trust %s, names %s\n", d.Name, d.ID, res.Relay, res.Approval, res.Trust, res.Names)
		if len(res.SignedOut) > 0 {
			fmt.Printf("Also signed out its controller record %s\n", strings.Join(res.SignedOut, ", "))
		}
		return nil
	})
}

// resolveDevice accepts a device id, short name or name; a name that
// several devices share lists them.
func resolveDevice(list []wire.Device, query string) (wire.Device, error) {
	var found []wire.Device
	for _, d := range list {
		if d.ID == query {
			return d, nil
		}
		if strings.EqualFold(d.Short, query) || strings.EqualFold(d.Name, query) {
			found = append(found, d)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return wire.Device{}, failf(wire.CodeNotFound, "no device %q (see the devices in Settings › Machines)", query)
	}
	candidates := make([]string, len(found))
	for i, d := range found {
		candidates[i] = fmt.Sprintf("%s (%s %s)", d.ID, d.Name, d.Role)
	}
	return wire.Device{}, failf(codeAmbiguous, "%q names several devices; use the id: %s", query, strings.Join(candidates, ", "))
}
