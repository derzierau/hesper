package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/derzierau/hesper/relay/pkg/client"
	"github.com/derzierau/hesper/relay/pkg/e2e"
	"github.com/derzierau/hesper/relay/pkg/protocol"
	"github.com/derzierau/hesper/relay/pkg/session"
)

func home() string {
	dir, _ := os.UserHomeDir()
	return dir
}
func stateDir() string { return filepath.Join(home(), ".local/state/hesper") }

// defaultCredentials keeps the historical ./controller.credentials.json when
// present, otherwise uses the installed controller credentials.
func defaultCredentials() string {
	if _, err := os.Stat("controller.credentials.json"); err == nil {
		return "controller.credentials.json"
	}
	return filepath.Join(stateDir(), "controller.credentials.json")
}

// machineIdentity is how a machine is named and shown (machines.json).
type machineIdentity struct {
	Short string `json:"short"`
	Glyph string `json:"glyph"`
	Color string `json:"color"`
}

type machinesConfig struct {
	Machines map[string]machineIdentity `json:"machines"`
}

// machineInfo is one inventory entry with its machineIdentity.
type machineInfo struct {
	protocol.Machine
	machineIdentity
	Local bool `json:"local"`
	// Route: "direct" or "relay", how requests reach it now (the fleet
	// sync's when one runs).
	Route string            `json:"route,omitempty"`
	state *session.Snapshot `json:"-"`
}

// machineColors are theme roles given out in machine order when machines.json
// does not name one.
var machineColors = []string{"focus", "branch", "elsewhere", "ask"}

func machinesPath() string { return filepath.Join(home(), ".config/hesper/machines.json") }

func loadMachinesConfig(path string) (machinesConfig, error) {
	config := machinesConfig{Machines: map[string]machineIdentity{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return config, nil
	}
	if err != nil {
		return config, err
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return config, fmt.Errorf("%s: %w", path, err)
	}
	if config.Machines == nil {
		config.Machines = map[string]machineIdentity{}
	}
	return config, nil
}

// defaultShort is the machine name lowercased up to the first space or "·".
func defaultShort(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.IndexFunc(name, func(r rune) bool { return unicode.IsSpace(r) || r == '·' }); i >= 0 {
		name = name[:i]
	}
	return strings.ToLower(name)
}

// identify applies machines.json, filling gaps with the default rules.
func identify(machines []protocol.Machine, saved map[string]machineIdentity, localID string) []machineInfo {
	sorted := append([]protocol.Machine(nil), machines...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	list := make([]machineInfo, 0, len(sorted))
	for i, m := range sorted {
		id := saved[m.ID]
		if id.Short == "" {
			id.Short = defaultShort(m.Name)
		}
		if id.Short == "" {
			id.Short = strings.ToLower(m.ID[:min(8, len(m.ID))])
		}
		if id.Glyph == "" {
			r, _ := utf8.DecodeRuneInString(id.Short)
			id.Glyph = strings.ToUpper(string(r))
		}
		if id.Color == "" {
			id.Color = machineColors[i%len(machineColors)]
		}
		info := machineInfo{Machine: m, machineIdentity: id, Local: m.ID == localID}
		var state session.Snapshot
		if len(m.Snapshot) > 0 && json.Unmarshal(m.Snapshot, &state) == nil {
			info.state = &state
		}
		list = append(list, info)
	}
	return list
}

func inventory(ctx context.Context, c *client.Controller) ([]machineInfo, error) {
	requestCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	machines, err := c.Machines(requestCtx)
	if err != nil {
		return nil, err
	}
	config, err := loadMachinesConfig(machinesPath())
	if err != nil {
		return nil, err
	}
	return identify(machines, config.Machines, hostDeviceID(filepath.Join(stateDir(), "host.credentials.json"))), nil
}

// hostDeviceID is the relay device ID in a credentials file ("" without).
func hostDeviceID(path string) string {
	c, err := client.LoadCredentials(path)
	if err != nil {
		return ""
	}
	return c.DeviceID
}

// resolveMachine accepts a machine ID, short name or full name.
func resolveMachine(list []machineInfo, query string) (machineInfo, error) {
	var found []machineInfo
	for _, m := range list {
		if m.ID == query {
			return m, nil
		}
		if strings.EqualFold(m.Short, query) || strings.EqualFold(m.Name, query) {
			found = append(found, m)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return machineInfo{}, fmt.Errorf("no machine %q (see hesperctl machines)", query)
	}
	return machineInfo{}, fmt.Errorf("%q names several machines; use the machine ID", query)
}

// machinesCommand lists the inventory with machineIdentity; --save writes the
// identities to machines.json, keeping entries already there.
func machinesCommand(ctx context.Context, f *flag.FlagSet, args []string) error {
	credentials := f.String("credentials", defaultCredentials(), "Controller credentials file")
	save := f.Bool("save", false, "Write the identities to ~/.config/hesper/machines.json")
	asJSON := f.Bool("json", false, "Print JSON")
	if err := f.Parse(args); err != nil {
		return err
	}
	c, err := connectController(ctx, *credentials)
	if err != nil {
		return err
	}
	defer c.Close()
	list, err := inventory(ctx, c)
	if err != nil {
		return err
	}
	routes := map[string]string{}
	if c.ViaFleet() {
		routes, _ = c.FleetRoutes(ctx)
	}
	for i := range list {
		if !list[i].Online || list[i].Local {
			continue
		}
		list[i].Route = routes[list[i].ID]
		if list[i].Route == "" {
			list[i].Route = c.Route(list[i].ID)
		}
		if list[i].Route == "" {
			list[i].Route = client.RouteRelay
		}
	}
	if *save {
		path := machinesPath()
		config, err := loadMachinesConfig(path)
		if err != nil {
			return err
		}
		for _, m := range list {
			if _, ok := config.Machines[m.ID]; !ok {
				config.Machines[m.ID] = m.machineIdentity
			}
		}
		data, _ := json.MarshalIndent(config, "", "  ")
		if err := writeFile(path, append(data, '\n'), 0644); err != nil {
			return err
		}
		if !*asJSON {
			fmt.Fprintln(os.Stderr, "Saved", path)
		}
	}
	if *asJSON {
		return output(list)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "\tSHORT\tNAME\tSTATE\tROUTE\tAGENTS\tMEMORY\tBATTERY\tID")
	for _, m := range list {
		state, agents, memory, battery, route := "offline", "-", "-", "-", "-"
		if m.Route != "" {
			route = m.Route
		}
		if m.Online {
			state = "online"
		}
		if m.Local {
			state += " (this Mac)"
		}
		if m.state != nil && m.state.Machine != nil {
			s := m.state.Machine
			agents = fmt.Sprint(s.Agents)
			if s.MemoryUsed != nil {
				memory = fmt.Sprintf("%.0f%%", *s.MemoryUsed*100)
			}
			if s.Battery != nil {
				battery = fmt.Sprintf("%.0f%%", *s.Battery*100)
				if s.OnBattery != nil && *s.OnBattery {
					battery += " on battery"
				}
			}
			if s.LidClosed != nil && *s.LidClosed {
				battery += ", lid closed"
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", m.Glyph, m.Short, m.Name, state, route, agents, memory, battery, m.ID)
	}
	return w.Flush()
}

func writeFile(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".hesperctl-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err = temp.Write(data); err == nil {
		err = temp.Chmod(mode)
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

// Pinned host keys (trust on first use, pkg/e2e): one X25519 key per host
// for transfers, approval codes and the end-to-end channel.
type trustedHost = e2e.TrustedHost
type trustFile = e2e.Trust

func trustPath() string { return filepath.Join(stateDir(), e2e.TrustFile) }

func loadTrust(path string) (trustFile, error) { return e2e.LoadTrust(path) }

// trustCommand shows pinned host keys or forgets one.
func trustCommand(f *flag.FlagSet, args []string) error {
	reset := f.String("reset", "", "Forget the pinned key of this machine (ID, short or full name)")
	asJSON := f.Bool("json", false, "Print JSON")
	if err := f.Parse(args); err != nil {
		return err
	}
	path := trustPath()
	trust, err := loadTrust(path)
	if err != nil {
		return err
	}
	if *reset != "" {
		var matches []string
		for id, h := range trust.Hosts {
			if id == *reset || strings.EqualFold(h.Name, *reset) || strings.EqualFold(defaultShort(h.Name), *reset) {
				matches = append(matches, id)
			}
		}
		if len(matches) != 1 {
			return fmt.Errorf("%d pinned machines match %q", len(matches), *reset)
		}
		if err := e2e.UpdateTrust(path, func(t *trustFile) (bool, error) {
			delete(t.Hosts, matches[0])
			return true, nil
		}); err != nil {
			return err
		}
		if *asJSON {
			return output(map[string]string{"reset": matches[0]})
		}
		fmt.Println("Forgot the key of", matches[0], "; pair again (hesperctl pair-host) to pin its current key and compare the code.")
		return nil
	}
	if *asJSON {
		return output(trust.Hosts)
	}
	if len(trust.Hosts) == 0 {
		fmt.Println("No host keys pinned yet.")
		return nil
	}
	ids := make([]string, 0, len(trust.Hosts))
	for id := range trust.Hosts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tPINNED\tE2E\tDIRECT\tKEY\tID")
	for _, id := range ids {
		h := trust.Hosts[id]
		channel, direct := "-", "-"
		if h.E2E > 0 {
			channel = "since " + time.Unix(h.E2E, 0).Format("2006-01-02")
		}
		if h.Direct > 0 {
			direct = "last " + time.Unix(h.Direct, 0).Format("2006-01-02")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", h.Name, time.Unix(h.Pinned, 0).Format("2006-01-02"), channel, direct, h.Key, id)
	}
	return w.Flush()
}
