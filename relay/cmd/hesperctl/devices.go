package main

// Device keys (docs/remote-shell-contract.md, Part K).
//
// On the controller: `hesperctl pair-host --machine M` asks host M to approve
// this device's keys and prints the code to compare.
// On the host: `hesperctl approve [--deny] NAME|CODE` decides a waiting
// request, `hesperctl
// devices-local [--revoke NAME]` lists or revokes approved controllers. Both
// work only on the host's local files; nothing travels through the relay.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/derzierau/hesper/relay/internal/host"
	"github.com/derzierau/hesper/relay/pkg/devicekey"
	"github.com/derzierau/hesper/relay/pkg/e2e"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

// parseInterspersed parses flags given before or after positional arguments.
func parseInterspersed(f *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := f.Parse(args); err != nil {
			return nil, err
		}
		args = f.Args()
		if len(args) == 0 {
			return positional, nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}

func parseRights(value string) []string {
	var rights []string
	for _, r := range strings.Split(value, ",") {
		if r = strings.TrimSpace(r); r != "" {
			rights = append(rights, r)
		}
	}
	return rights
}

func defaultDeviceName() string {
	name, _ := os.Hostname()
	name = strings.TrimSuffix(strings.TrimSuffix(name, ".local"), ".lan")
	if !host.ValidName(name) {
		return "controller"
	}
	return name
}

// pairHostCommand asks a host to approve this device's keys.
func pairHostCommand(ctx context.Context, f *flag.FlagSet, args []string) error {
	credentials := f.String("credentials", defaultCredentials(), "Controller credentials file")
	machine := f.String("machine", "", "Host to ask (ID, short or full name)")
	rights := f.String("rights", "observe,answer,type,transfer", "Rights to ask for: "+strings.Join(devicekey.AllRights, ","))
	name := f.String("name", defaultDeviceName(), "This device's name on the host")
	wait := f.Duration("wait", 0, "Wait this long for the approval (0: print the code and return)")
	asJSON := f.Bool("json", false, "Print JSON")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *machine == "" {
		return fmt.Errorf("--machine is required")
	}
	if !host.ValidName(*name) {
		return fmt.Errorf("--name must be 1 to 40 printable characters")
	}
	signer := deviceSigner()
	if signer == nil {
		return fmt.Errorf("this machine has no device keys: %v", devicekey.ErrNoHelper)
	}
	device, err := signer.PublicKey(false)
	if err != nil {
		return err
	}
	strong, err := signer.PublicKey(true)
	if err != nil {
		return err
	}
	deviceKey, _ := devicekey.EncodePublicKey(device)
	strongKey, _ := devicekey.EncodePublicKey(strong)
	_, deviceDER, _ := devicekey.ParsePublicKey(deviceKey)
	_, strongDER, _ := devicekey.ParsePublicKey(strongKey)
	c, err := connectController(ctx, *credentials)
	if err != nil {
		return err
	}
	defer c.Close()
	list, err := inventory(ctx, c)
	if err != nil {
		return err
	}
	m, err := resolveMachine(list, *machine)
	if err != nil {
		return err
	}
	request := host.ApprovalRequest{Name: *name, Key: deviceKey, StrongKey: strongKey, Hardware: signer.Hardware(), Rights: parseRights(*rights)}
	// The end-to-end key (part N) and the device key's signature of it:
	// the host keeps it with the approval.
	if id, err := e2e.LoadIdentity(devicekey.StateDir()); err == nil {
		if e2e.VerifyBinding(device, id.Public, id.Binding) != nil {
			_ = id.Bind(signer)
		}
		if id.Binding != "" {
			request.E2EKey, request.E2EBinding = base64.StdEncoding.EncodeToString(id.Public), id.Binding
		}
	}
	params, _ := devicekey.NormalizeParams(protocol.JSON(request))
	ask := func() (host.ApprovalStatus, error) {
		requestCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		var status host.ApprovalStatus
		// devices.request is not signed: it only asks the host's user.
		raw, err := c.Forward(requestCtx, m.ID, "devices.request", params, nil)
		if err == nil {
			err = json.Unmarshal(raw, &status)
		}
		return status, err
	}
	status, err := ask()
	if err != nil {
		return err
	}
	hostKey, err := base64.StdEncoding.Strict().DecodeString(status.HostKey)
	if err != nil || len(hostKey) != 32 {
		return fmt.Errorf("%s sent no valid host key", m.Short)
	}
	// The host key is pinned like for transfers; the code covers it, so a
	// relay that swaps it shows a different code here than on the host.
	if err := e2e.UpdateTrust(trustPath(), func(trust *trustFile) (bool, error) {
		if pinned, ok := trust.Hosts[m.ID]; ok && pinned.Key != status.HostKey {
			return false, e2e.KeyChanged(m.Short, pinned)
		} else if ok {
			return false, nil
		}
		trust.Hosts[m.ID] = trustedHost{Name: m.Name, Key: status.HostKey, Pinned: time.Now().Unix()}
		return true, nil
	}); err != nil {
		return err
	}
	code := devicekey.ApprovalCode(hostKey, deviceDER, strongDER)
	if status.Status == "pending" && status.Code != code {
		return fmt.Errorf("the code from %s (%s) is not the code of these keys (%s): something between the machines changed a key; do not approve", m.Short, status.Code, code)
	}
	report := func(status host.ApprovalStatus) error {
		if *asJSON {
			return output(map[string]any{"machine": m.ID, "short": m.Short, "status": status.Status, "code": code, "rights": status.Rights, "name": status.Name})
		}
		if status.Status == "approved" {
			fmt.Printf("%s approved this device as %q with rights %s.\n", m.Short, status.Name, strings.Join(status.Rights, ", "))
			return nil
		}
		fmt.Printf("Code: %s\nOn %s, check that it shows the same code, then press C-a A in Hesper or run:\n  hesperctl approve %s\n", code, m.Short, code)
		return nil
	}
	if status.Status != "pending" || *wait <= 0 {
		return report(status)
	}
	if !*asJSON {
		fmt.Printf("Code: %s\nOn %s, check that it shows the same code, then press C-a A in Hesper or run `hesperctl approve %s`.\nWaiting for the approval…\n", code, m.Short, code)
	}
	deadline := time.Now().Add(*wait)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(6 * time.Second):
		}
		next, err := ask()
		var fault *protocol.Error
		if errors.As(err, &fault) && fault.Code == "busy" {
			continue
		}
		if err != nil {
			return err
		}
		if next.HostKey != status.HostKey {
			return fmt.Errorf("%s changed its host key while waiting; do not approve", m.Short)
		}
		if next.Status == "approved" {
			return report(next)
		}
	}
	return fmt.Errorf("not approved within %s; the request stays open for 10 minutes", *wait)
}

func hostStore(dir string) host.Store { return host.Store{Dir: dir} }

// approveCommand decides a waiting request on this host.
func approveCommand(f *flag.FlagSet, args []string) error {
	deny := f.Bool("deny", false, "Deny instead of approving")
	rights := f.String("rights", "", "Grant these rights instead of the requested ones (comma separated)")
	name := f.String("name", "", "Name the device differently")
	softwareShell := f.Bool("allow-software-shell", false, "Grant shell to a device without hardware (Secure Enclave) keys")
	dir := f.String("state-dir", devicekey.StateDir(), "Host state directory")
	asJSON := f.Bool("json", false, "Print JSON")
	positional, err := parseInterspersed(f, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: hesperctl approve [--deny] [--rights R,…] [--name N] NAME|CODE")
	}
	store := hostStore(*dir)
	if *deny {
		p, err := store.Deny(positional[0], time.Now())
		if err != nil {
			return err
		}
		if *asJSON {
			return output(map[string]any{"denied": p})
		}
		fmt.Printf("Denied %q (%s).\n", p.Name, p.Code)
		return nil
	}
	var opts host.ApproveOptions
	if *rights != "" {
		opts.Rights = parseRights(*rights)
	}
	opts.Name, opts.AllowSoftwareShell = *name, *softwareShell
	c, note, err := store.Approve(positional[0], opts, time.Now())
	if err != nil {
		return err
	}
	if *asJSON {
		return output(map[string]any{"approved": c, "note": note})
	}
	fmt.Printf("Approved %q with rights %s.\n", c.Name, strings.Join(c.Rights, ", "))
	if note != "" {
		fmt.Println("Note:", note)
	}
	return nil
}

// devicesLocalCommand lists this host's approved and waiting devices.
func devicesLocalCommand(f *flag.FlagSet, args []string) error {
	revoke := f.String("revoke", "", "Revoke this approved controller (name or device ID)")
	dir := f.String("state-dir", devicekey.StateDir(), "Host state directory")
	asJSON := f.Bool("json", false, "Print JSON")
	if err := f.Parse(args); err != nil {
		return err
	}
	store := hostStore(*dir)
	if *revoke != "" {
		c, err := store.Revoke(*revoke)
		if err != nil {
			return err
		}
		if *asJSON {
			return output(map[string]any{"revoked": c})
		}
		fmt.Printf("Revoked %q; its requests are refused from now on.\n", c.Name)
		return nil
	}
	list, enforcing, err := store.LoadControllers()
	if err != nil {
		return err
	}
	pending, err := store.PendingRequests(time.Now())
	if err != nil {
		return err
	}
	if *asJSON {
		return output(map[string]any{"controllers": list.Controllers, "pending": pending, "enforcing": enforcing})
	}
	var b bytes.Buffer
	w := tabwriter.NewWriter(&b, 0, 2, 2, ' ', 0)
	if !enforcing {
		fmt.Fprintln(&b, "No approved controllers: this host accepts unsigned requests until one is approved (or runs with --require-device-keys).")
	} else if len(list.Controllers) == 0 {
		fmt.Fprintln(&b, "No approved controllers: this host refuses every request but snapshot (all were revoked).")
	} else {
		fmt.Fprintln(w, "NAME\tRIGHTS\tHARDWARE\tE2E KEY\tAPPROVED\tDEVICE")
		for _, c := range list.Controllers {
			key := "(bound at first channel)"
			if c.E2EKey != "" {
				key = c.E2EKey[:min(12, len(c.E2EKey))] + "…"
			}
			fmt.Fprintf(w, "%s\t%s\t%t\t%s\t%s\t%s\n", c.Name, strings.Join(c.Rights, ","), c.Hardware, key, time.Unix(c.Approved, 0).Format("2006-01-02"), c.Device)
		}
		w.Flush()
	}
	if len(pending) > 0 {
		fmt.Fprintln(&b, "\nWaiting for approval (hesperctl approve CODE, or --deny):")
		w = tabwriter.NewWriter(&b, 0, 2, 2, ' ', 0)
		fmt.Fprintln(w, "CODE\tNAME\tRIGHTS\tHARDWARE\tEXPIRES")
		for _, p := range pending {
			fmt.Fprintf(w, "%s\t%s\t%s\t%t\t%s\n", p.Code, p.Name, strings.Join(p.Rights, ","), p.Hardware, time.Unix(p.Expires, 0).Format("15:04"))
		}
		w.Flush()
	}
	_, err = os.Stdout.Write(b.Bytes())
	return err
}
