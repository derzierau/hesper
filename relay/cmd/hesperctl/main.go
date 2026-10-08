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
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
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
func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: hesperctl ls|new|send|approve|deny|stop|resume|attach|mv|rm|rename (agents, on hesperd)\n       hesperctl login|invite|devices|revoke|pair|machines|watch|request|trust|pair-host|approve-device|devices-local [flags] (relay)")
	}
	if isAgentCommand(args) {
		if args[0] == "approve" {
			return approveOrDevice(ctx, flag.NewFlagSet("approve", flag.ContinueOnError), args)
		}
		return agentCommand(ctx, args)
	}
	// The relay command an agent command took the name of.
	if args[0] == "approve-device" {
		args = append([]string{"approve"}, args[1:]...)
	}
	command := args[0]
	f := flag.NewFlagSet(command, flag.ContinueOnError)
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
	case "approve":
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
				return fmt.Errorf("--device is required")
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
			return fmt.Errorf("--name and --out are required")
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
			return fmt.Errorf("--name and --out are required")
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
			return fmt.Errorf("--machine is required")
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
