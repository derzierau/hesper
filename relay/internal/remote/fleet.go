// Package remote is hesperd's controller role (rebuild contract part R):
// the local daemon's gateway to every other machine's hesperd. It holds
// this Mac's controller connection to the relay (sign-in and renewal,
// device keys, the end-to-end channel, the direct path) and implements
// agents.Remote:
//
//   - one link per other machine (pkg/agentlink, internal/host): the host's
//     agent events arrive on it at once and feed a merged registry, so the
//     app's agents.subscribe shows remote agents like local ones, with the
//     machine's short name (machines.json) as id prefix;
//   - every agents method for a remote agent becomes one signed request
//     inside the channel (direct path when the firewall allows, else the
//     relay);
//   - attaching to a remote agent is a channel on the link, bridged byte
//     for byte to the local attach connection (bridge.go);
//   - agents.move carries an agent with its conversation and code between
//     any two machines (move.go).
//
// It also serves the control socket (internal/controlsock) for hesperctl's
// relay commands, which must not open a second connection with the same
// enrollment.
package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/derzierau/hesper/relay/internal/agents"
	"github.com/derzierau/hesper/relay/internal/controlsock"
	"github.com/derzierau/hesper/relay/internal/projects"
	"github.com/derzierau/hesper/relay/pkg/client"
	"github.com/derzierau/hesper/relay/pkg/devicekey"
	"github.com/derzierau/hesper/relay/pkg/e2e"
	"github.com/derzierau/hesper/relay/pkg/protocol"
	"github.com/derzierau/hesper/relay/pkg/session"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Options configure a Fleet.
type Options struct {
	// Credentials is the controller credentials file (renewed in place).
	Credentials string
	// HostDeviceID is this Mac's own host enrollment: not another machine.
	HostDeviceID string
	// ConfigDir holds machines.json (short names by relay device id).
	ConfigDir string
	// StateDir holds the device keys, e2e.key and trusted-hosts.json
	// (devicekey.StateDir()); moves stage bundles below it.
	StateDir string
	// Local is this Mac's registry (moves pack and import here).
	Local *agents.Registry
	// Signer signs requests (this Mac's device keys); nil: unsigned.
	Signer devicekey.Signer
	// Direct configures the direct path (zero: defaults).
	Direct client.DirectConfig
	// ControlSocket (optional) serves hesperctl's relay commands.
	ControlSocket string
	// Grace keeps an unreachable machine's agents listed (default 60 s).
	Grace time.Duration
	Logf  func(format string, args ...any)

	// Projects (optional; projects step 1) is this Mac's project
	// registry, replicated with the other machines (projects.go).
	Projects *projects.Store
	// Sessions (optional; shared history) hears of links and hints
	// (sessions.go).
	Sessions SessionsLinks
}

// Fleet is the controller role. Run connects; the agents.Remote methods
// serve the local daemon.
type Fleet struct {
	opt     Options
	control *controlsock.Server

	mu       sync.Mutex
	c        *client.Controller
	creds    protocol.Credentials
	machines map[string]*machine // by relay device id
	watchers map[*watcher]struct{}
	changed  chan struct{} // closed when a link or connection changes
	ready    chan struct{} // closed with the first inventory

	files fileUploads // attachments being forwarded (files.go)
}

// machine is another machine of this owner.
type machine struct {
	id, name, short string
	online          bool
	state           session.Snapshot
	link            *link // current link, nil while there is none
	linking         bool
	retry           time.Time
	hostShort       string                // the host's name for itself
	agents          map[string]wire.Agent // by local id, in this Mac's naming
	lost            *time.Timer           // drops agents after the grace
	rtts            []time.Duration
	rttRoute        string
}

type watcher struct {
	changed func(wire.Agent)
	removed func(id, reason, to string)
}

// New makes a Fleet; Run connects it.
func New(opt Options) *Fleet {
	if opt.Logf == nil {
		opt.Logf = log.Printf
	}
	if opt.Grace <= 0 {
		opt.Grace = time.Minute
	}
	if opt.StateDir == "" {
		opt.StateDir = devicekey.StateDir()
	}
	f := &Fleet{opt: opt, machines: map[string]*machine{}, watchers: map[*watcher]struct{}{}, changed: make(chan struct{}), ready: make(chan struct{})}
	if c, err := client.LoadCredentials(opt.Credentials); err == nil {
		f.control = controlsock.New(c.DeviceID)
	}
	return f
}

// SetLocal gives the Fleet this Mac's registry (before Run).
func (f *Fleet) SetLocal(reg *agents.Registry) { f.opt.Local = reg }

// Run keeps the relay connection up until ctx ends; it returns early only
// when the credentials need a new sign-in.
func (f *Fleet) Run(ctx context.Context) error {
	if f.opt.ControlSocket != "" && f.control != nil {
		l, err := controlsock.Listen(f.opt.ControlSocket)
		if err != nil {
			f.opt.Logf("control socket: %v", err)
		} else {
			defer l.Close()
			go f.control.Serve(ctx, l)
		}
	}
	identity, err := e2e.LoadIdentity(f.opt.StateDir)
	if err != nil {
		return fmt.Errorf("cannot load this machine's end-to-end key: %w", err)
	}
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		creds, err := client.FreshCredentials(ctx, f.opt.Credentials)
		if errors.Is(err, client.ErrSignInRequired) {
			return err
		}
		if errors.Is(err, client.ErrRenewalPostponed) && time.Until(creds.ExpiresAt) > 0 {
			f.opt.Logf("remote: %v", err)
			err = nil
		}
		var c *client.Controller
		if err == nil {
			c, err = client.NewController(ctx, creds)
		}
		if err == nil {
			if f.opt.Signer != nil {
				c.SetSigner(f.opt.Signer)
			}
			c.EnableE2E(client.E2EConfig{Identity: identity, TrustPath: filepath.Join(f.opt.StateDir, e2e.TrustFile), Signer: f.opt.Signer,
				Notice: func(text string) { f.opt.Logf("remote: %s", text) }})
			direct := f.opt.Direct
			direct.OnRoute = func(id, route string) { f.rerouted(id, route) }
			c.EnableDirect(direct)
			f.serve(ctx, c, creds)
			c.Close()
		} else {
			var fault *protocol.Error
			if errors.As(err, &fault) && fault.Code == "unauthorized" {
				return client.SignInRequired(f.opt.Credentials, creds, err)
			}
			f.opt.Logf("remote: %v", err)
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second
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

// serve runs one relay connection: inventory updates, links, pings.
func (f *Fleet) serve(ctx context.Context, c *client.Controller, creds protocol.Credentials) {
	f.mu.Lock()
	f.c, f.creds = c, creds
	f.notifyLocked()
	f.mu.Unlock()
	f.control.Set(c)
	defer func() {
		f.control.Set(nil)
		f.mu.Lock()
		f.c = nil
		for _, m := range f.machines {
			if m.link != nil {
				m.link.close()
			}
		}
		f.notifyLocked()
		f.mu.Unlock()
	}()
	pctx, pcancel := context.WithCancel(ctx)
	defer pcancel()
	go f.syncProjectsLoop(pctx, c) // projects step 1
	pings := time.NewTicker(pingEvery)
	defer pings.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case machines, ok := <-c.Updates():
			if !ok {
				return
			}
			f.control.Publish(machines)
			f.inventory(c, machines)
		case <-tick.C:
			f.relink(c)
		case <-pings.C:
			go f.ping(ctx, c)
		}
	}
}

// Machine identities (machines.json): short names by relay device id.
type machinesConfig struct {
	Machines map[string]struct {
		Short string `json:"short"`
	} `json:"machines"`
}

func (f *Fleet) loadConfig() machinesConfig {
	var cfg machinesConfig
	if data, err := os.ReadFile(filepath.Join(f.opt.ConfigDir, "machines.json")); err == nil {
		json.Unmarshal(data, &cfg)
	}
	return cfg
}

// defaultShort is a machine name lowercased up to the first space, dot or
// "·".
func defaultShort(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.IndexFunc(name, func(r rune) bool { return unicode.IsSpace(r) || r == '·' || r == '.' }); i >= 0 {
		name = name[:i]
	}
	return strings.ToLower(strings.ReplaceAll(name, "/", "-"))
}

func (f *Fleet) localShort() string {
	if f.opt.Local != nil {
		return f.opt.Local.Machine()
	}
	return ""
}

// inventory takes the relay's machine list.
func (f *Fleet) inventory(c *client.Controller, list []protocol.Machine) {
	cfg := f.loadConfig()
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := map[string]bool{}
	taken := map[string]bool{f.localShort(): true}
	sorted := slices.Clone(list)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	for _, pm := range sorted {
		if pm.ID == f.opt.HostDeviceID {
			continue
		}
		seen[pm.ID] = true
		m := f.machines[pm.ID]
		if m == nil {
			m = &machine{id: pm.ID, agents: map[string]wire.Agent{}}
			f.machines[pm.ID] = m
		}
		m.name, m.online = pm.Name, pm.Online
		m.state = session.Snapshot{}
		if len(pm.Snapshot) > 0 {
			json.Unmarshal(pm.Snapshot, &m.state)
		}
		short := cfg.Machines[pm.ID].Short
		if short == "" {
			short = m.state.Short
		}
		if short == "" {
			short = defaultShort(pm.Name)
		}
		if short == "" {
			short = strings.ToLower(pm.ID[:min(8, len(pm.ID))])
		}
		short = strings.ReplaceAll(short, "/", "-")
		for base, n := short, 2; taken[short]; n++ {
			short = fmt.Sprintf("%s%d", base, n)
		}
		taken[short] = true
		if m.short != "" && m.short != short {
			f.renameLocked(m, short)
		}
		m.short = short
		if !m.online && m.link != nil {
			m.link.close()
		}
	}
	for id, m := range f.machines {
		if !seen[id] {
			if m.link != nil {
				m.link.close()
			}
			f.dropAgentsLocked(m)
			delete(f.machines, id)
		}
	}
	select {
	case <-f.ready:
	default:
		close(f.ready)
	}
	f.relinkLocked(c)
}

// renameLocked moves a machine's agents to its new short name.
func (f *Fleet) renameLocked(m *machine, short string) {
	old := m.agents
	m.agents = map[string]wire.Agent{}
	for local, a := range old {
		f.emitRemovedLocked(a.ID)
		a.ID, a.Machine = short+"/"+local, short
		m.agents[local] = a
		f.emitChangedLocked(a)
	}
}

func (f *Fleet) relink(c *client.Controller) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.relinkLocked(c)
}

// relinkLocked opens a link to every online hesperd host without one.
func (f *Fleet) relinkLocked(c *client.Controller) {
	if f.c != c || c == nil {
		return
	}
	now := time.Now()
	for _, m := range f.machines {
		if !m.online || !m.state.Capabilities.Agents || !m.state.Capabilities.E2E || m.linking || now.Before(m.retry) {
			continue
		}
		// No link yet, or one on the relay while the direct path is up
		// (it came up while the link was being opened): a new link,
		// which replaces the old one.
		if m.link == nil || m.link.route != client.RouteDirect && c.Route(m.id) == client.RouteDirect {
			m.linking = true
			go f.openLink(c, m)
		}
	}
}

// rerouted: the direct path to a machine came up (or went): a link on the
// relay moves to the direct path (the next link opened goes there).
func (f *Fleet) rerouted(id, route string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.machines[id]
	if m == nil || f.c == nil {
		return
	}
	if route == client.RouteDirect && m.link != nil && m.link.route != client.RouteDirect && !m.linking {
		m.linking = true
		go f.openLink(f.c, m)
	}
	m.rtts, m.rttRoute = nil, ""
	f.notifyLocked()
}

// notifyLocked wakes everyone waiting for a link or connection change.
func (f *Fleet) notifyLocked() {
	close(f.changed)
	f.changed = make(chan struct{})
}

func (f *Fleet) emitChangedLocked(a wire.Agent) {
	for w := range f.watchers {
		w.changed(a)
	}
}

func (f *Fleet) emitRemovedLocked(id string) {
	f.emitRemovedReasonLocked(id, "")
}

// emitRemovedReasonLocked: a removal with the host's reason (closing
// agents: "closed", "finished-in-background", "removed").
func (f *Fleet) emitRemovedReasonLocked(id, reason string) {
	f.emitRemovedToLocked(id, reason, "")
}

// emitRemovedToLocked: a removal with its reason and, for a move, the
// agent it became.
func (f *Fleet) emitRemovedToLocked(id, reason, to string) {
	for w := range f.watchers {
		w.removed(id, reason, to)
	}
}

func (f *Fleet) dropAgentsLocked(m *machine) {
	for local, a := range m.agents {
		delete(m.agents, local)
		f.emitRemovedLocked(a.ID)
	}
}

// named is a host's agent in this Mac's naming.
func (m *machine) named(a wire.Agent) wire.Agent {
	a.ID = m.short + "/" + localID(a.ID)
	a.Machine = m.short
	return a
}

func localID(id string) string {
	if _, l, ok := strings.Cut(id, "/"); ok {
		return l
	}
	return id
}

// --- agents.Remote ---

// Machines are the other machines, for hello.
func (f *Fleet) Machines() []wire.Machine {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []wire.Machine{}
	for _, m := range f.machines {
		w := wire.Machine{Short: m.short, Name: m.name, Online: m.online && f.c != nil}
		if w.Online {
			w.Route = f.c.Route(m.id)
			if r := median(m.rtts); r > 0 && m.rttRoute == w.Route {
				w.RTTMs = int(math.Round(float64(r) / float64(time.Millisecond)))
			}
		}
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Short < out[j].Short })
	return out
}

// Agents are the other machines' agents.
func (f *Fleet) Agents() []wire.Agent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.agentsLocked()
}

func (f *Fleet) agentsLocked() []wire.Agent {
	out := []wire.Agent{}
	for _, m := range f.machines {
		for _, a := range m.agents {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Created.Equal(out[j].Created) {
			return out[i].Created.Before(out[j].Created)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Watch reports remote agents (all of them first) and their changes until
// ctx ends.
func (f *Fleet) Watch(ctx context.Context, changed func(wire.Agent), removed func(id, reason, to string)) {
	w := &watcher{changed: changed, removed: removed}
	f.mu.Lock()
	for _, a := range f.agentsLocked() {
		changed(a)
	}
	f.watchers[w] = struct{}{}
	f.mu.Unlock()
	<-ctx.Done()
	f.mu.Lock()
	delete(f.watchers, w)
	f.mu.Unlock()
}

// machineByShort finds a machine by its short name.
func (f *Fleet) machineByShort(short string) (*machine, *client.Controller, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.machines {
		if m.short == short {
			if f.c == nil || !m.online {
				return m, nil, wire.Errorf(wire.CodeUnavailable, "machine %s is not reachable right now", short)
			}
			return m, f.c, nil
		}
	}
	if f.c == nil {
		return nil, nil, wire.Errorf(wire.CodeUnavailable, "not connected to the relay")
	}
	return nil, nil, wire.Errorf(wire.CodeNotFound, "no machine %s", short)
}

// wireError turns a host's answer into the local daemon's error codes.
func wireError(err error) error {
	if err == nil {
		return nil
	}
	var we *wire.Error
	if errors.As(err, &we) {
		return we
	}
	var fault *protocol.Error
	if errors.As(err, &fault) {
		switch fault.Code {
		case wire.CodeNotFound, wire.CodeInvalid, wire.CodeExists, wire.CodeUnavailable, wire.CodeForbidden, wire.CodeLive,
			wire.CodeTooLarge, wire.CodeNoRemote, wire.CodeToolMissing: // move work
			return &wire.Error{Code: fault.Code, Message: fault.Message}
		case "invalid_request":
			return &wire.Error{Code: wire.CodeInvalid, Message: fault.Message}
		case "e2e_required", "key_changed", "e2e_binding", "integrity":
			return &wire.Error{Code: wire.CodeForbidden, Message: fault.Message}
		case "timeout", "connection_lost", "busy", "shutdown", "expired":
			return &wire.Error{Code: wire.CodeUnavailable, Message: fault.Message}
		}
		return &wire.Error{Code: wire.CodeRemote, Message: fault.Message}
	}
	return &wire.Error{Code: wire.CodeRemote, Message: err.Error()}
}

// request sends one signed request to a machine through the channel.
func (f *Fleet) request(ctx context.Context, c *client.Controller, m *machine, method string, params any) (json.RawMessage, error) {
	raw, err := c.Request(client.RequireE2E(ctx), m.id, method, params)
	return raw, wireError(err)
}

// Call runs a method for a remote agent or machine.
func (f *Fleet) Call(ctx context.Context, short, method string, params json.RawMessage) (json.RawMessage, error) {
	m, c, err := f.machineByShort(short)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if len(params) > 0 && string(params) != "null" {
		if err := json.Unmarshal(params, &fields); err != nil {
			return nil, wire.Errorf(wire.CodeInvalid, "params must be an object")
		}
	}
	if fields == nil {
		fields = map[string]json.RawMessage{}
	}
	// The host knows its agents by local id and itself as "this machine".
	if raw, ok := fields["id"]; ok {
		var id string
		if json.Unmarshal(raw, &id) == nil {
			fields["id"], _ = json.Marshal(localID(id))
		}
	}
	delete(fields, "machine")
	raw, err := f.request(ctx, c, m, method, fields)
	if err != nil {
		return nil, err
	}
	switch method {
	case "agents.spawn", "agents.resume", "agents.rename":
		var a wire.Agent
		if json.Unmarshal(raw, &a) == nil && a.ID != "" {
			f.mu.Lock()
			a = m.named(a)
			// Shown at once; the link's event confirms it.
			m.agents[localID(a.ID)] = a
			f.emitChangedLocked(a)
			f.mu.Unlock()
			return protocol.JSON(a), nil
		}
	case "agents.remove":
		f.mu.Lock()
		if id, ok := fields["id"]; ok {
			var local string
			json.Unmarshal(id, &local)
			if a, ok := m.agents[local]; ok {
				delete(m.agents, local)
				f.emitRemovedReasonLocked(a.ID, wire.ReasonRemoved)
			}
		}
		f.mu.Unlock()
	}
	return raw, nil
}

// Route is how a machine's link runs: "direct", "relay", or "" without a
// link.
func (f *Fleet) Route(short string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.machines {
		if m.short == short && m.link != nil {
			return m.link.route
		}
	}
	return ""
}

// waitLink returns a machine's link, waiting up to wait for one.
func (f *Fleet) waitLink(ctx context.Context, short string, wait time.Duration) (*machine, *link, error) {
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		m, _, err := f.machineByShort(short)
		f.mu.Lock()
		var l *link
		if m != nil {
			l = m.link
		}
		changed := f.changed
		f.mu.Unlock()
		if l != nil {
			return m, l, nil
		}
		select {
		case <-changed:
		case <-deadline.C:
			if err == nil {
				err = wire.Errorf(wire.CodeUnavailable, "no link to %s yet", short)
			}
			return nil, nil, err
		case <-ctx.Done():
			return nil, nil, wire.Errorf(wire.CodeUnavailable, "no link to %s", short)
		}
	}
}

var _ agents.Remote = (*Fleet)(nil)
