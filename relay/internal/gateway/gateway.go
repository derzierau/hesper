// Package gateway assembles hesperd: the registry and its local socket
// (internal/agents), the host role (internal/host: this Mac's agents for
// approved devices, through the relay or the direct path) and the
// controller role (internal/remote: every other machine's agents through
// the local socket). `hesperd serve` runs one; tests run two against an
// in-process relay.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/internal/agents"
	"github.com/derzierau/hesper/relay/internal/handoff"
	"github.com/derzierau/hesper/relay/internal/host"
	"github.com/derzierau/hesper/relay/internal/projects"
	"github.com/derzierau/hesper/relay/internal/remote"
	"github.com/derzierau/hesper/relay/internal/sessions"
	"github.com/derzierau/hesper/relay/pkg/client"
	"github.com/derzierau/hesper/relay/pkg/devicekey"
	"github.com/derzierau/hesper/relay/pkg/protocol"
	"github.com/derzierau/hesper/relay/pkg/transfer"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Config is one hesperd.
type Config struct {
	// Registry configures the agents (StateDir, ConfigDir, Socket, …).
	Registry agents.Options
	// Listener (optional) is the local socket, else agents.Listen(Socket).
	Listener net.Listener
	// HostCredentials and ControllerCredentials are the relay enrollments
	// of the two roles; a role whose file is missing stays off.
	HostCredentials, ControllerCredentials string
	// KeysDir holds device keys, e2e.key, trusted-hosts.json and, for the
	// host role, controllers.json, the audit log and the replay cache
	// (default Registry.StateDir).
	KeysDir string
	// Signer is this Mac's device keys (default devicekey.Default for
	// KeysDir).
	Signer devicekey.Signer
	// AllowShell offers shells to devices with the shell right.
	AllowShell bool
	// RequireDeviceKeys enforces device keys before any approval.
	RequireDeviceKeys bool
	// Direct: "auto" (listen unless the macOS firewall would ask), "on",
	// "off"; DirectPort 0 is random; DirectAddrs (tests) replaces this
	// Mac's private addresses.
	Direct      string
	DirectPort  int
	DirectAddrs func() []netip.Addr
	// ControllerDirect configures the controller's side of the direct path.
	ControllerDirect client.DirectConfig
	// ControlSocket (optional) serves hesperctl's relay commands.
	ControlSocket string
	// Uploads stages moves (default KeysDir/uploads).
	Uploads string
	// KeepAwake holds an idle-sleep assertion while agents work (macOS).
	KeepAwake  bool
	MinBattery int
	// Grace keeps an unreachable machine's agents listed.
	Grace time.Duration
	// PollInterval is the host's safety-net publication interval.
	PollInterval time.Duration
	Logger       *slog.Logger
	// History tunes the shared history (scan intervals, mirror window,
	// foreground for tests); its folders come from Registry.
	History sessions.Options
	// NoHistory runs without the shared history.
	NoHistory bool
	// ScratchRoot holds the scratch projects (internal/projects,
	// scratch.go); default Registry.Home/scratch when Home is set, else
	// scratch projects are off.
	ScratchRoot string
}

// Daemon is a running hesperd.
type Daemon struct {
	Registry *agents.Registry
	// Projects is the project registry (projects step 1).
	Projects *projects.Store
	// History is the shared history (internal/sessions); nil with
	// NoHistory.
	History *sessions.Service
	Server  *agents.Server
	Fleet   *remote.Fleet // nil without controller credentials
	Service *host.Service // nil without host credentials
	Runner  *host.Runner  // nil without host credentials
	Auth    *host.Authorizer
	Handoff *host.Handoff
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	once    sync.Once
	// Err receives a role's fatal error (a sign-in that is needed).
	Err chan error
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return path != "" && err == nil
}

// Start opens the registry, serves the socket and starts the roles.
func Start(parent context.Context, cfg Config) (*Daemon, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	logf := func(format string, args ...any) { cfg.Logger.Info(fmt.Sprintf(format, args...)) }
	opt := cfg.Registry
	if opt.Logf == nil {
		opt.Logf = logf
	}
	if opt.StateDir == "" {
		opt.StateDir = devicekey.StateDir()
	}
	if cfg.KeysDir == "" {
		cfg.KeysDir = opt.StateDir
	}
	if cfg.Uploads == "" {
		cfg.Uploads = filepath.Join(cfg.KeysDir, "uploads")
	}
	if cfg.Signer == nil {
		if devicekey.SoftwareMode() || runtime.GOOS != "darwin" {
			cfg.Signer = &devicekey.Software{Dir: cfg.KeysDir}
		} else if s, err := devicekey.Default(); err == nil {
			cfg.Signer = s
		}
	}
	ctx, cancel := context.WithCancel(parent)
	d := &Daemon{cancel: cancel, Err: make(chan error, 2)}
	// projects step 1: the project registry, before the agents (restored
	// agents get their project).
	scratch := cfg.ScratchRoot
	if scratch == "" && opt.Home != "" {
		scratch = filepath.Join(opt.Home, "scratch")
	}
	d.Projects = projects.Open(projects.Options{StateDir: opt.StateDir, Machine: opt.Machine, Home: opt.Home, Logf: opt.Logf,
		ScratchRoot: scratch, ProjectsRoot: opt.ProjectsRoot, ConfigDir: opt.ConfigDir})
	opt.Projects = d.Projects
	// shared history: before the agents (the socket serves sessions.*).
	if !cfg.NoHistory {
		h := cfg.History
		h.StateDir, h.ConfigDir, h.ClaudeHome, h.CodexHome, h.UserHome, h.Machine = opt.StateDir, opt.ConfigDir, opt.ClaudeHome, opt.CodexHome, opt.Home, opt.Machine
		if h.Logf == nil {
			h.Logf = opt.Logf
		}
		hist, err := sessions.Open(h)
		if err != nil {
			logf("shared history off: %v", err)
		} else {
			d.History = hist
			opt.Sessions = hist
		}
	}
	if exists(cfg.ControllerCredentials) {
		hostID := ""
		if c, err := client.LoadCredentials(cfg.HostCredentials); err == nil {
			hostID = c.DeviceID
		}
		d.Fleet = remote.New(remote.Options{Credentials: cfg.ControllerCredentials, HostDeviceID: hostID, ConfigDir: opt.ConfigDir,
			StateDir: cfg.KeysDir, Signer: cfg.Signer, Direct: cfg.ControllerDirect, ControlSocket: cfg.ControlSocket, Grace: cfg.Grace, Logf: logf,
			Projects: d.Projects})
		opt.Remote = d.Fleet
	} else {
		logf("controller role off: no %s (hesperctl login --role controller)", cfg.ControllerCredentials)
	}
	ln := cfg.Listener
	if ln == nil {
		var err error
		if ln, err = agents.Listen(opt.Socket); err != nil {
			d.closeHistory()
			d.Projects.Close()
			cancel()
			return nil, err
		}
	}
	reg, err := agents.Open(opt)
	if err != nil {
		ln.Close()
		d.closeHistory()
		d.Projects.Close()
		cancel()
		return nil, err
	}
	d.Registry = reg
	d.wireProjects()
	d.wireHistory() // shared history
	d.Server = agents.NewServer(reg)
	d.wg.Add(1)
	go func() { defer d.wg.Done(); d.Server.Serve(ln) }()
	if d.Fleet != nil {
		d.Fleet.SetLocal(reg)
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			if err := d.Fleet.Run(ctx); err != nil {
				logf("controller role stopped: %v", err)
				d.Err <- err
			}
		}()
	}
	if exists(cfg.HostCredentials) {
		if err := d.startHost(ctx, cfg, logf); err != nil {
			d.Close()
			return nil, err
		}
	} else {
		logf("host role off: no %s (hesperctl login --role host)", cfg.HostCredentials)
	}
	return d, nil
}

func (d *Daemon) startHost(ctx context.Context, cfg Config, logf func(string, ...any)) error {
	reg := d.Registry
	hc, err := client.LoadCredentials(cfg.HostCredentials)
	if err != nil {
		return fmt.Errorf("host credentials: %w", err)
	}
	key, err := transfer.LoadOrCreateKey(filepath.Join(filepath.Dir(cfg.HostCredentials), "host.transfer.key"))
	if err != nil {
		return fmt.Errorf("transfer key: %w", err)
	}
	auth := &host.Authorizer{Store: host.Store{Dir: cfg.KeysDir}, MachineID: hc.DeviceID, HostKey: key.PublicKey().Bytes(), Require: cfg.RequireDeviceKeys}
	if runtime.GOOS == "darwin" {
		auth.Notify = host.MacNotify
	}
	if enforcing, err := auth.Enforcing(); err != nil {
		logf("device allowlist is damaged; every request but snapshot will be refused: %v", err)
	} else if !enforcing {
		logf("device keys not enforced: no approved controllers yet (hesperctl approve on this Mac after pair-host on the other)")
	}
	d.Auth = auth
	// Everything but the relay-visible snapshot and pings goes through the
	// channel (agent names, tasks and screens never travel in plaintext).
	channel := &host.E2E{Key: key, Auth: auth, Require: true}
	d.Handoff = &host.Handoff{Dir: cfg.Uploads, Key: key, Logger: cfg.Logger,
		Pack: func(ctx context.Context, id string, have []string, dir string) error {
			if strings.HasPrefix(id, sessions.ExportPrefix) && d.History != nil {
				// shared history; its codes (live, not_found, …) reach
				// the requester.
				if err := d.History.Pack(ctx, id, have, dir); err != nil {
					var we *wire.Error
					if errors.As(err, &we) {
						return protocol.Err(we.Code, we.Message)
					}
					return err
				}
				return nil
			}
			if strings.HasPrefix(id, agents.FolderExportPrefix) {
				// bring the folder: a folder, not an agent.
				path, changes, err := agents.ParseFolderExport(id)
				if err == nil {
					_, err = reg.PackFolder(ctx, path, changes, have, dir)
				}
				var we *wire.Error
				if errors.As(err, &we) {
					return protocol.Err(we.Code, we.Message)
				}
				return err
			}
			_, err := reg.Pack(ctx, id, have, dir)
			return err
		},
		Import: func(ctx context.Context, dir string) (json.RawMessage, error) {
			if m, err := handoff.LoadManifest(dir); err == nil && m.Bring != nil {
				// bring the folder: the folder made, no agent started.
				res, err := reg.ImportFolder(ctx, dir)
				if err != nil {
					return nil, err
				}
				return json.Marshal(res)
			}
			a, err := reg.Import(ctx, dir)
			if err != nil {
				return nil, err
			}
			return json.Marshal(a)
		}}
	if err := os.MkdirAll(cfg.Uploads, 0o700); err != nil {
		return err
	}
	if err := d.Handoff.Start(ctx); err != nil {
		return fmt.Errorf("moves: %w", err)
	}
	changes := make(chan struct{}, 1)
	changed := func() {
		select {
		case changes <- struct{}{}:
		default:
		}
	}
	var direct *host.Direct
	switch cfg.Direct {
	case "off":
	case "", "auto", "on":
		ok := true
		if cfg.Direct != "on" && runtime.GOOS == "darwin" && cfg.DirectAddrs == nil {
			exe, _ := os.Executable()
			if verdict := host.MacFirewall(exe, nil); !verdict.Allowed {
				ok = false
				logf("direct path off: %s; controllers use the relay (allow it: %s)", verdict.Reason, host.FirewallAdvice(exe))
			}
		}
		if ok {
			direct = &host.Direct{E2E: channel, Port: cfg.DirectPort, Addrs: cfg.DirectAddrs, Logger: cfg.Logger}
		}
	default:
		return errors.New("direct must be on, off or auto")
	}
	d.Service = &host.Service{Agents: reg, Handoff: d.Handoff, Machine: &host.MachineMonitor{},
		AllowShell: cfg.AllowShell, E2EKey: channel.PublicKey(), Direct: direct, Audit: auth.Audit, Projects: d.Projects}
	if d.History != nil { // shared history
		d.Service.Sessions = d.History
		d.History.SetHostKey(key)
	}
	poll := cfg.PollInterval
	if poll <= 0 {
		poll = 30 * time.Second
	}
	d.Runner = &host.Runner{Events: changes, Credentials: hc, CredentialsPath: cfg.HostCredentials, Auth: auth, E2E: channel, Direct: direct,
		Service: d.Service, PollInterval: poll, Logger: cfg.Logger}
	// Every agent change republishes the relay-visible snapshot (push).
	sub := reg.Subscribe()
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		defer reg.Unsubscribe(sub)
		for sub.Wait(ctx.Done()) != nil {
			changed()
		}
	}()
	if cfg.KeepAwake {
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			(&host.KeepAwake{Snapshot: d.Service.Snapshot, MinBattery: cfg.MinBattery, Logger: cfg.Logger}).Run(ctx)
		}()
	}
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		defer d.Handoff.Wait()
		if err := d.Runner.Run(ctx); err != nil {
			logf("host role stopped: %v", err)
			d.Err <- err
		}
	}()
	return nil
}

// Close stops the roles, the socket and the agents (they resume on the
// next start).
func (d *Daemon) Close() {
	d.once.Do(func() {
		d.cancel()
		if d.Runner != nil && d.Runner.Direct != nil {
			d.Runner.Direct.Stop()
		}
		if d.Server != nil {
			d.Server.Close()
		}
		if d.Registry != nil {
			d.Registry.Close()
		}
		d.wg.Wait()
		d.closeHistory()
		if d.Projects != nil {
			d.Projects.Close()
		}
	})
}
