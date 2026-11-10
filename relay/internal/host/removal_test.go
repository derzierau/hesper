package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/derzierau/hesper/relay/pkg/devicekey"
	"github.com/derzierau/hesper/relay/pkg/e2e"
	"github.com/derzierau/hesper/relay/pkg/protocol"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// fakeRelay is the controller connection's view of the relay.
type fakeRelay struct {
	connected, supports bool
	devices             []protocol.Device
	devicesErr          error
	revokeErr           map[string]error
	revoked             []string
}

func (f *fakeRelay) Connected() bool { return f.connected }
func (f *fakeRelay) Supports(feature string) bool {
	return f.supports && feature == protocol.FeatureDevices
}
func (f *fakeRelay) Devices(context.Context) ([]protocol.Device, error) {
	if f.devicesErr != nil {
		return nil, f.devicesErr
	}
	return slices.Clone(f.devices), nil
}
func (f *fakeRelay) Revoke(_ context.Context, id string) error {
	if err := f.revokeErr[id]; err != nil {
		return err
	}
	f.revoked = append(f.revoked, id)
	return nil
}

func TestMergeDevicesUnionsAllSources(t *testing.T) {
	relay := []protocol.Device{
		{ID: "self-c", Name: "This Mac", Role: "controller", Online: true},
		{ID: "self-h", Name: "This Mac", Role: "host", Online: true},
		{ID: "stale-h", Name: "Old Laptop", Role: "host"},
		{ID: "phone", Name: "Phone", Role: "controller", Online: true, Push: true},
		{ID: "b-h", Name: "Beta", Role: "host", Online: true},
	}
	controllers := []Controller{{Device: "ctl-here", Name: "Gamma"}, {Device: "phone", Name: "Phone"}}
	trust := map[string]e2e.TrustedHost{"b-h": {Name: "Beta"}, "pinned-only": {Name: "Delta"}}
	names := map[string]string{"b-h": "beta", "stale-h": "old", "self-h": "me"}
	got := MergeDevices(relay, true, controllers, trust, names, "self-h", "self-c")
	ids := []string{}
	for _, d := range got {
		ids = append(ids, d.ID)
	}
	// this Mac (host first), Macs by short name or name, iPhones by name
	want := []string{"self-h", "self-c", "b-h", "pinned-only", "ctl-here", "stale-h", "phone"}
	if !slices.Equal(ids, want) {
		t.Fatalf("order %v, want %v", ids, want)
	}
	by := map[string]wire.Device{}
	for _, d := range got {
		by[d.ID] = d
	}
	if d := by["self-h"]; !d.Self || d.Short != "me" || !d.Relay {
		t.Errorf("self host %+v", d)
	}
	if !by["self-c"].Self || by["b-h"].Self {
		t.Errorf("self flags %+v %+v", by["self-c"], by["b-h"])
	}
	if d := by["stale-h"]; d.Online || !d.Relay || d.Short != "old" || d.Role != "host" || d.Kind != wire.DeviceMac {
		t.Errorf("stale relay-only host %+v", d)
	}
	if d := by["ctl-here"]; !d.ApprovedHere || d.Relay || d.Role != "controller" || d.Kind != wire.DeviceMac || d.Name != "Gamma" {
		t.Errorf("controller approved here, unknown to the relay %+v", d)
	}
	if d := by["phone"]; d.Kind != wire.DeviceIPhone || !d.ApprovedHere || !d.Online {
		t.Errorf("iPhone by push %+v", d)
	}
	if d := by["b-h"]; !d.TrustedHere || d.Kind != wire.DeviceMac {
		t.Errorf("pinned host %+v", d)
	}
	if d := by["pinned-only"]; !d.TrustedHere || d.Relay || d.Role != "host" || d.Name != "Delta" {
		t.Errorf("pinned only %+v", d)
	}
	// Without the relay's list only this Mac's files count.
	local := MergeDevices(relay, false, controllers, nil, nil, "self-h", "self-c")
	if len(local) != 2 || local[0].Relay || local[0].Online {
		t.Errorf("relay unknown: %+v", local)
	}
}

// removalEnv is a Mac's state and config with one device in every file.
type removalEnv struct {
	svc   *DevicesService
	relay *fakeRelay
	store Store
}

func newRemovalEnv(t *testing.T) *removalEnv {
	t.Helper()
	dir := t.TempDir()
	store := Store{Dir: filepath.Join(dir, "state")}
	keyA, keyB := publicKeys(t, &devicekey.Software{Dir: t.TempDir()})
	keyC, keyD := publicKeys(t, &devicekey.Software{Dir: t.TempDir()})
	rights := []string{devicekey.AllRights[0]}
	if err := store.saveControllers(Controllers{Controllers: []Controller{
		{Device: "dev-x", Name: "X", Key: keyA, StrongKey: keyB, Rights: rights},
		{Device: "ctl-w", Name: "Workbook", Key: keyC, StrongKey: keyD, Rights: rights},
	}}); err != nil {
		t.Fatal(err)
	}
	trustPath := filepath.Join(store.Dir, e2e.TrustFile)
	if err := e2e.SaveTrust(trustPath, e2e.Trust{Hosts: map[string]e2e.TrustedHost{"dev-x": {Name: "X"}, "host-w": {Name: "Workbook"}}}); err != nil {
		t.Fatal(err)
	}
	machines := filepath.Join(dir, "config", "machines.json")
	os.MkdirAll(filepath.Dir(machines), 0o700)
	os.WriteFile(machines, []byte(`{"machines":{"dev-x":{"short":"x","glyph":"X","color":"focus"},"host-w":{"short":"wb"},"keep":{"short":"k"}},"other":1}`), 0o644)
	relay := &fakeRelay{connected: true, supports: true, devices: []protocol.Device{
		{ID: "self-c", Name: "Me", Role: "controller", Online: true},
		{ID: "dev-x", Name: "X", Role: "controller"},
		{ID: "host-w", Name: "Workbook", Role: "host"},
		{ID: "ctl-w", Name: "Workbook", Role: "controller"},
		{ID: "ctl-w2", Name: "workbook", Role: "controller", Online: true},
	}}
	svc := &DevicesService{Store: store, TrustPath: trustPath, MachinesPath: machines, SelfHost: "self-h", SelfController: "self-c", Relay: relay}
	return &removalEnv{svc: svc, relay: relay, store: store}
}

func (e *removalEnv) state(t *testing.T) (approved, pinned, named []string) {
	t.Helper()
	list, _, err := e.store.LoadControllers()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range list.Controllers {
		approved = append(approved, c.Device)
	}
	trust, _ := e2e.LoadTrust(e.svc.TrustPath)
	for id := range trust.Hosts {
		pinned = append(pinned, id)
	}
	names, err := loadMachineNames(e.svc.MachinesPath)
	if err != nil {
		t.Fatal(err)
	}
	for id := range names {
		named = append(named, id)
	}
	slices.Sort(approved)
	slices.Sort(pinned)
	slices.Sort(named)
	return
}

func TestRemoveRefusesSelf(t *testing.T) {
	e := newRemovalEnv(t)
	for _, id := range []string{"self-h", "self-c"} {
		for _, local := range []bool{false, true} {
			if _, err := e.svc.Remove(context.Background(), id, local); wireCode(err) != wire.CodeForbidden {
				t.Errorf("%s local %v: %v", id, local, err)
			}
		}
	}
	if len(e.relay.revoked) != 0 {
		t.Errorf("revoked %v", e.relay.revoked)
	}
	if _, err := e.svc.Remove(context.Background(), "nobody", false); wireCode(err) != wire.CodeNotFound {
		t.Errorf("unknown: %v", err)
	}
	if _, err := e.svc.Remove(context.Background(), "", false); wireCode(err) != wire.CodeInvalid {
		t.Errorf("empty: %v", err)
	}
}

func TestRemoveNeedsTheRelayUnlessLocalOnly(t *testing.T) {
	e := newRemovalEnv(t)
	a0, p0, n0 := e.state(t)
	unchanged := func(what string) {
		t.Helper()
		a, p, n := e.state(t)
		if !slices.Equal(a, a0) || !slices.Equal(p, p0) || !slices.Equal(n, n0) {
			t.Errorf("%s changed local state: %v %v %v", what, a, p, n)
		}
	}
	e.relay.connected = false
	if _, err := e.svc.Remove(context.Background(), "dev-x", false); wireCode(err) != wire.CodeUnavailable {
		t.Errorf("disconnected: %v", err)
	}
	unchanged("disconnected")
	e.svc.Relay = nil
	if _, err := e.svc.Remove(context.Background(), "dev-x", false); wireCode(err) != wire.CodeUnavailable {
		t.Errorf("no controller role: %v", err)
	}
	e.svc.Relay = e.relay
	e.relay.connected, e.relay.supports = true, false
	_, err := e.svc.Remove(context.Background(), "dev-x", false)
	if wireCode(err) != wire.CodeUnsupported || !strings.Contains(err.Error(), "needs an update") {
		t.Errorf("old relay: %v", err)
	}
	unchanged("old relay")
	e.relay.supports = true
	e.relay.revokeErr = map[string]error{"dev-x": protocol.Err("rate_limited", "slow down")}
	if _, err := e.svc.Remove(context.Background(), "dev-x", false); wireCode(err) != "rate_limited" {
		t.Errorf("revoke error: %v", err)
	}
	unchanged("revoke error")
	// localOnly: the relay is not asked.
	e.relay.connected = false
	res, err := e.svc.Remove(context.Background(), "dev-x", true)
	if err != nil || !sameRemoval(res, wire.DeviceRemoval{Relay: wire.StepSkipped, Approval: wire.StepDone, Trust: wire.StepDone, Names: wire.StepDone}) {
		t.Fatalf("local only: %+v %v", res, err)
	}
	if a, p, n := e.state(t); slices.Contains(a, "dev-x") || slices.Contains(p, "dev-x") || slices.Contains(n, "dev-x") {
		t.Errorf("local only left %v %v %v", a, p, n)
	}
}

func sameRemoval(a, b wire.DeviceRemoval) bool {
	return a.Relay == b.Relay && a.Approval == b.Approval && a.Trust == b.Trust && a.Names == b.Names
}

func TestRemoveRunsEveryLocalStep(t *testing.T) {
	e := newRemovalEnv(t)
	// not_found from revoke right after the relay listed it: already gone.
	e.relay.revokeErr = map[string]error{"dev-x": protocol.Err("not_found", "no such device")}
	res, err := e.svc.Remove(context.Background(), "dev-x", false)
	if err != nil || !sameRemoval(res, wire.DeviceRemoval{Relay: wire.StepDone, Approval: wire.StepDone, Trust: wire.StepDone, Names: wire.StepDone}) {
		t.Fatalf("remove: %+v %v", res, err)
	}
	a, p, n := e.state(t)
	if !slices.Equal(a, []string{"ctl-w"}) || !slices.Equal(p, []string{"host-w"}) || !slices.Equal(n, []string{"host-w", "keep"}) {
		t.Errorf("left %v %v %v", a, p, n)
	}
	// machines.json keeps everything else.
	data, _ := os.ReadFile(e.svc.MachinesPath)
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil || string(raw["other"]) != "1" || strings.Contains(string(data), "dev-x") || !strings.Contains(string(data), `"short": "wb"`) {
		t.Errorf("machines.json: %s", data)
	}
	audit, _ := os.ReadFile(filepath.Join(e.store.Dir, AuditFile))
	if !strings.Contains(string(audit), `"event":"device.removed","device":"dev-x"`) || !strings.Contains(string(audit), "relay done, approval done, trust done, names done") {
		t.Errorf("audit:\n%s", audit)
	}
	// A device only this Mac knows: the relay step is skipped.
	res, err = e.svc.Remove(context.Background(), "keep", false)
	if err != nil || !sameRemoval(res, wire.DeviceRemoval{Relay: wire.StepSkipped, Approval: wire.StepSkipped, Trust: wire.StepSkipped, Names: wire.StepDone}) {
		t.Errorf("device only this Mac knows: %+v %v", res, err)
	}
	if len(e.relay.revoked) != 0 {
		t.Errorf("revoked %v", e.relay.revoked)
	}
}

// Names are not unique ("MacBook Pro"), and nothing binds a host id to
// a controller id: removing a host revokes that id and no other.
func TestRemovingAHostRevokesOnlyThatHost(t *testing.T) {
	e := newRemovalEnv(t)
	res, err := e.svc.Remove(context.Background(), "host-w", false)
	if err != nil {
		t.Fatal(err)
	}
	want := wire.DeviceRemoval{Relay: wire.StepDone, Approval: wire.StepSkipped, Trust: wire.StepDone, Names: wire.StepDone}
	if !sameRemoval(res, want) {
		t.Fatalf("result %+v, want %+v", res, want)
	}
	// the offline controller of the same name may be another Mac
	if !slices.Equal(e.relay.revoked, []string{"host-w"}) {
		t.Errorf("revoked %v", e.relay.revoked)
	}
	if a, _, _ := e.state(t); !slices.Equal(a, []string{"ctl-w", "dev-x"}) {
		t.Errorf("approved %v", a)
	}
}

// A relay that cannot be asked is said, not hidden behind this Mac's files.
func TestDevicesListSaysWhenTheRelayCouldNotBeAsked(t *testing.T) {
	e := newRemovalEnv(t)
	list := func() wire.DevicesList {
		t.Helper()
		res, err := e.svc.Call(context.Background(), "devices.list", nil)
		if err != nil {
			t.Fatal(err)
		}
		return res.(wire.DevicesList)
	}
	if l := list(); l.RelayUnavailable != "" {
		t.Errorf("relay answered: %q", l.RelayUnavailable)
	}
	e.relay.devicesErr = errors.New("timed out")
	if l := list(); !strings.Contains(l.RelayUnavailable, "timed out") || len(l.Devices) == 0 {
		t.Errorf("relay error: %+v", l)
	}
	e.relay.devicesErr, e.relay.connected = nil, false
	if l := list(); l.RelayUnavailable == "" {
		t.Errorf("disconnected: %+v", l)
	}
	e.relay.connected, e.relay.supports = true, false
	if l := list(); l.RelayUnavailable == "" {
		t.Errorf("old relay: %+v", l)
	}
	e.svc.Relay = nil // no controller role: no relay to ask
	if l := list(); l.RelayUnavailable != "" {
		t.Errorf("no relay: %+v", l)
	}
}

// Removing a name keeps machines.json's mode.
func TestDropMachineNameKeepsTheMode(t *testing.T) {
	e := newRemovalEnv(t)
	if err := os.Chmod(e.svc.MachinesPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Remove(context.Background(), "keep", true); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(e.svc.MachinesPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
}

func TestDevicesCall(t *testing.T) {
	e := newRemovalEnv(t)
	res, err := e.svc.Call(context.Background(), "devices.list", nil)
	list, ok := res.(wire.DevicesList)
	if err != nil || !ok || len(list.Devices) != 6 || !list.Devices[0].Self {
		t.Fatalf("devices.list: %+v %v", res, err)
	}
	res, err = e.svc.Call(context.Background(), "devices.remove", json.RawMessage(`{"deviceId":"keep","localOnly":true}`))
	if r, _ := res.(wire.DeviceRemoval); err != nil || r.Names != wire.StepDone || r.Relay != wire.StepSkipped {
		t.Fatalf("devices.remove: %+v %v", res, err)
	}
	if _, err := e.svc.Call(context.Background(), "devices.remove", json.RawMessage(`[1]`)); wireCode(err) != wire.CodeInvalid {
		t.Errorf("bad params: %v", err)
	}
}

// Controller names come from the device: one named like another's id
// must not block or redirect the removal of that id.
func TestRemoveMatchesTheApprovalByIDOnly(t *testing.T) {
	e := newRemovalEnv(t)
	list, _, err := e.store.LoadControllers()
	if err != nil {
		t.Fatal(err)
	}
	for i := range list.Controllers {
		if list.Controllers[i].Device == "ctl-w" {
			list.Controllers[i].Name = "dev-x"
		}
	}
	if err := e.store.saveControllers(list); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.Remove(context.Background(), "dev-x", true)
	if err != nil || res.Approval != wire.StepDone {
		t.Fatalf("remove: %+v %v", res, err)
	}
	if a, _, _ := e.state(t); !slices.Equal(a, []string{"ctl-w"}) {
		t.Errorf("approved %v", a)
	}
}
