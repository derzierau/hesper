package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/derzierau/hesper/relay/pkg/client"
	"github.com/derzierau/hesper/relay/pkg/devicekey"
	"github.com/derzierau/hesper/relay/pkg/e2e"
)

// The relay keeps one connection per device, so a command connecting with
// the controller credentials would replace hesperd's (whose reconnect would
// then replace the command's). hesperd therefore serves a control socket
// (internal/controlsock) and these commands send their requests through
// its connection; only without a running hesperd does a command connect
// itself.

func defaultControlSocket() string { return filepath.Join(stateDir(), "controller.sock") }

// route is how a command reaches the relay: through the fleet sync's control
// socket, or with --direct its own connection.
type route struct {
	direct *bool
	socket *string
}

type routeKey struct{}

func addRouteFlags(ctx context.Context, f *flag.FlagSet) context.Context {
	return context.WithValue(ctx, routeKey{}, route{
		direct: f.Bool("direct", false, "Connect to the relay directly even when hesperd runs (that replaces hesperd's connection)"),
		socket: f.String("socket", defaultControlSocket(), "Control socket of a running hesperd"),
	})
}

// connectController uses a running hesperd for these credentials when
// there is one, else connects to the relay.
func connectController(ctx context.Context, path string) (*client.Controller, error) {
	return connect(ctx, path, false)
}

func connect(ctx context.Context, path string, watch bool) (*client.Controller, error) {
	c, err := connectUnsigned(ctx, path, watch)
	if err == nil {
		// Requests are signed here, in the command's own process, also when
		// they go through the fleet sync (which only forwards them).
		if signer := deviceSigner(); signer != nil {
			c.SetSigner(signer)
		}
	}
	return c, err
}

// deviceSigner is this machine's device keys, nil when it has none (no
// hesper-keys helper on a Mac): requests then go unsigned and only hosts
// that do not enforce device keys yet accept them.
var deviceSigner = func() devicekey.Signer {
	s, err := devicekey.Default()
	if err != nil {
		return nil
	}
	return s
}

func connectUnsigned(ctx context.Context, path string, watch bool) (*client.Controller, error) {
	if r, ok := ctx.Value(routeKey{}).(route); ok && !*r.direct && *r.socket != "" {
		if creds, err := client.LoadCredentials(path); err == nil {
			c, err := client.DialFleet(ctx, *r.socket, creds.DeviceID, watch)
			if err == nil {
				// The fleet holds the channels; this process needs its
				// static key and the pins only to connect a terminal
				// stream the host opened on the direct path.
				_ = enableE2E(c)
			}
			if !errors.Is(err, client.ErrNoFleet) {
				return c, err
			}
		}
	}
	creds, err := client.FreshCredentials(ctx, path)
	if errors.Is(err, client.ErrRenewalPostponed) && time.Until(creds.ExpiresAt) > 0 {
		err = nil
	}
	if err != nil {
		return nil, err
	}
	c, err := client.NewController(ctx, creds)
	if err != nil {
		return nil, err
	}
	if err := enableE2E(c); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// enableE2E turns on the end-to-end channel (contract part N) for a
// controller with its own relay connection: this machine's static key
// (e2e.key next to the device keys), bound to the device key, and the host
// keys pinned in trusted-hosts.json. Without the key nothing is sent: a
// controller without the channel would send shell requests in plaintext.
func enableE2E(c *client.Controller) error {
	id, err := e2e.LoadIdentity(devicekey.StateDir())
	if err != nil {
		return fmt.Errorf("cannot load this machine's end-to-end key: %w", err)
	}
	c.EnableE2E(client.E2EConfig{Identity: id, TrustPath: trustPath(), Signer: deviceSigner(),
		Notice: func(text string) { fmt.Fprintln(os.Stderr, text) }})
	return nil
}
