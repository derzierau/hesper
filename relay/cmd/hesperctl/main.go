package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/derzierau/hesper/relay/pkg/protocol"

	"github.com/derzierau/hesper/relay/pkg/client"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	code := execute(ctx, os.Args[1:], os.Stderr)
	stop()
	os.Exit(code)
}

// The relay and device commands (the commands are in commands.go).
func init() {
	route := "Requests go through a running hesperd's connection (--socket); --direct connects to the relay itself, replacing hesperd's connection meanwhile."
	for _, c := range []Command{
		{Name: "machines", Summary: "List the owner's machines (relay inventory)", Usage: "machines [--save] [--json]",
			Help:     "Short name, name, online state, route (direct or relay) and load of every machine. --save writes their names and glyphs to ~/.config/hesper/machines.json. " + route,
			Output:   "[{id, name, online, snapshot, short, glyph, color, local, route}]",
			Examples: []string{"hesperctl machines", "hesperctl machines --json | jq -r '.[] | select(.online) | .short'"}},
		{Name: "trust", Summary: "Show pinned host keys, or forget one", Usage: "trust [--reset MACHINE] [--json]",
			Output: "{machineId: {name, key, pinned, e2e, direct}}, with --reset {reset: machineId}"},
		{Name: "pair-host", Summary: "Ask a host to approve this device's keys", Usage: "pair-host --machine M [--rights R,…] [--wait D] [--json]",
			Help:   "Prints the code to compare on the host, where approve CODE (or C-a A in Hesper) approves it. " + route,
			Output: "{machine, short, status, code, rights, name}"},
		{Name: "approve-device", Summary: "Approve or deny a device waiting on this host", Usage: "approve-device [--deny] [--rights R,…] [--name N] NAME|CODE [--json]",
			Help:   "Works on this host's files only. approve with these flags, or with a code no agent has, does the same.",
			Output: "{approved, note} or {denied}"},
		{Name: "devices-local", Summary: "List or revoke the controllers this host approved", Usage: "devices-local [--revoke NAME] [--json]",
			Output: "{controllers, pending, enforcing}, with --revoke {revoked}"},
		{Name: "login", Summary: "Enroll this device with the relay (GitHub sign-in)", Usage: "login --name NAME --out FILE [--role R] [--relay URL]",
			Output: "{deviceId, role, credentialsFile}"},
		{Name: "pair", Summary: "Enroll with an invitation (development relays)", Usage: "pair --relay URL --name NAME --out FILE [--invitation-file F]",
			Output: "{deviceId, role, credentialsFile}"},
		{Name: "invite", Summary: "Create an invitation (relay admin socket)", Usage: "invite [--owner O] [--role R] [--admin-socket S]"},
		{Name: "devices", Summary: "List the relay's devices (relay admin socket)", Usage: "devices [--admin-socket S]"},
		{Name: "revoke", Summary: "Revoke a device (relay admin socket)", Usage: "revoke --device ID [--admin-socket S]"},
		{Name: "watch", Summary: "Print the machine inventory as it changes", Usage: "watch [--credentials F]",
			Help: "One JSON inventory per change, until interrupted. " + route, Output: "a stream of machine inventories"},
		{Name: "request", Summary: "Send one host operation through the relay", Usage: "request --machine ID [--method M] [--params FILE|-]",
			Help: route, Output: "the host's result"},
	} {
		c.Group = groupRelay
		c.Run = relayRun(c.Name)
		register(c)
	}
}

// relayRun is the Run of the relay command `command`.
func relayRun(command string) func(context.Context, *flag.FlagSet, []string) error {
	return func(ctx context.Context, f *flag.FlagSet, args []string) error {
		return relayCommand(ctx, f, command, args)
	}
}
func output(value any) error {
	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", "  ")
	return e.Encode(value)
}
func read(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(io.LimitReader(os.Stdin, 64*1024))
	}
	return os.ReadFile(path)
}
func relayCommand(ctx context.Context, f *flag.FlagSet, command string, args []string) error {
	// The cases below take args with the command first.
	args = append([]string{command}, args...)
	switch command {
	case "machines", "watch", "request", "pair-host":
		ctx = addRouteFlags(ctx, f)
	}
	switch command {
	case "machines":
		return machinesCommand(ctx, f, args[1:])
	case "trust":
		return trustCommand(f, args[1:])
	case "pair-host":
		return pairHostCommand(ctx, f, args[1:])
	case "approve-device":
		return approveCommand(f, args[1:])
	case "devices-local":
		return devicesLocalCommand(f, args[1:])
	case "invite", "devices", "revoke":
		socket := f.String("admin-socket", ".state/admin.sock", "Local relay admin socket")
		owner := f.String("owner", "personal", "Owner namespace")
		role := f.String("role", "host", "host or controller")
		device := f.String("device", "", "Device ID to revoke")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		method, path := http.MethodGet, "/v1/devices"
		var body []byte
		if command == "invite" {
			method, path = http.MethodPost, "/v1/invitations"
			body, _ = json.Marshal(map[string]string{"owner": *owner, "role": *role})
		}
		if command == "revoke" {
			if *device == "" {
				return usagef("--device is required")
			}
			method, path = http.MethodDelete, "/v1/devices/"+url.PathEscape(*device)
		}
		h := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", *socket)
		}}}
		req, err := http.NewRequestWithContext(ctx, method, "http://localhost"+path, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := h.Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		if err != nil {
			return err
		}
		if res.StatusCode >= 400 {
			return fmt.Errorf("admin request failed (%d): %s", res.StatusCode, data)
		}
		_, err = os.Stdout.Write(append(data, '\n'))
		return err
	case "login":
		origin := f.String("relay", "https://relay.olezierau.de", "Relay HTTPS origin")
		name := f.String("name", "", "Device display name")
		role := f.String("role", "controller", "host or controller")
		out := f.String("out", "", "New credentials file")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if *name == "" || *out == "" {
			return usagef("--name and --out are required")
		}
		if _, err := os.Lstat(*out); !os.IsNotExist(err) {
			return fmt.Errorf("credentials destination must not exist")
		}
		e, verifier, err := client.BeginLogin(ctx, *origin, *name, *role, "")
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Open %s\nCompare approval code: %s\n", e.VerificationURL, e.Code)
		loginCtx, cancel := context.WithDeadline(ctx, e.Expires)
		defer cancel()
		ticker := time.NewTicker(time.Duration(max(e.Interval, 5)) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-loginCtx.Done():
				return loginCtx.Err()
			case <-ticker.C:
				c, err := client.ClaimLogin(loginCtx, *origin, e.ID, verifier)
				var fault *protocol.Error
				if errors.As(err, &fault) && fault.Code == "authorization_pending" {
					continue
				}
				if err != nil {
					return err
				}
				if err = client.SaveCredentials(*out, c); err != nil {
					return fmt.Errorf("approved device %s could not be saved; revoke it and sign in again: %w", c.DeviceID, err)
				}
				return output(map[string]string{"deviceId": c.DeviceID, "role": c.Role, "credentialsFile": *out})
			}
		}
	case "pair":
		relay := f.String("relay", "", "Relay HTTPS origin")
		invitation := f.String("invitation-file", "-", "Read invitation string or invite JSON from file; - reads stdin")
		name := f.String("name", "", "Device display name")
		out := f.String("out", "", "New credentials file (mode 0600)")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if *out == "" || *name == "" {
			return usagef("--name and --out are required")
		}
		if _, err := os.Lstat(*out); err == nil {
			return fmt.Errorf("credentials file already exists")
		} else if !os.IsNotExist(err) {
			return err
		}
		data, err := read(*invitation)
		if err != nil {
			return err
		}
		token := strings.TrimSpace(string(data))
		if strings.HasPrefix(token, "{") {
			var v struct {
				Invitation string `json:"invitation"`
			}
			if err := json.Unmarshal(data, &v); err != nil {
				return err
			}
			token = v.Invitation
		}
		c, err := client.Pair(ctx, *relay, token, *name)
		if err != nil {
			return err
		}
		if err := client.SaveCredentials(*out, c); err != nil {
			return fmt.Errorf("enrollment succeeded but credentials could not be saved; revoke device %s and pair again: %w", c.DeviceID, err)
		}
		return output(map[string]string{"deviceId": c.DeviceID, "role": c.Role, "credentialsFile": *out})
	case "watch", "request":
		credentials := f.String("credentials", "controller.credentials.json", "Controller credentials file")
		machine := f.String("machine", "", "Machine ID")
		method := f.String("method", "snapshot", "Host operation")
		params := f.String("params", "", "JSON parameters file, or - for stdin (default {})")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if command == "watch" {
			return watch(ctx, *credentials)
		}
		c, err := connectController(ctx, *credentials)
		if err != nil {
			return err
		}
		defer c.Close()
		requestCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		if *machine == "" {
			return usagef("--machine is required")
		}
		var payload json.RawMessage = []byte("{}")
		if *params != "" {
			payload, err = read(*params)
			if err != nil {
				return err
			}
		}
		if !json.Valid(payload) {
			return fmt.Errorf("invalid parameter JSON")
		}
		result, err := c.Request(requestCtx, *machine, *method, payload)
		if err != nil {
			return err
		}
		return output(result)
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}

// watch reconnects observation only. Mutating requests are never replayed.
func watch(ctx context.Context, path string) error {
	backoff := time.Second
	for ctx.Err() == nil {
		if _, err := client.LoadCredentials(path); err != nil {
			return err
		}
		c, err := connect(ctx, path, true)
		if err == nil {
			backoff = time.Second
			active := true
			for active {
				select {
				case <-ctx.Done():
					c.Close()
					return nil
				case machines, ok := <-c.Updates():
					if !ok {
						active = false
						break
					}
					if err = output(machines); err != nil {
						c.Close()
						return err
					}
				}
			}
			c.Close()
		} else {
			var fault *protocol.Error
			if errors.As(err, &fault) && fault.Code == "unauthorized" {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff = min(30*time.Second, backoff*2)
	}
	return nil
}
