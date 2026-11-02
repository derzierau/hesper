package main

// `hesperctl phone`: the iPhone's way onto this Mac (docs/iphone/
// onboarding.md). It asks hesperd for a pairing offer (devices.offer),
// draws its code in the terminal, renews it every two minutes and exits
// when a device used one of its offers (devices.paired).

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/derzierau/hesper/relay/internal/host"
	"github.com/derzierau/hesper/relay/internal/qr"
	"github.com/derzierau/hesper/relay/pkg/devicekey"
)

func init() {
	register(Command{Name: "phone", Group: groupRelay, NoMCP: true, Summary: "Show a code that pairs an iPhone with this Mac",
		Usage: "phone [--rights R,…] [--json]",
		Help: "Draws a pairing code to scan with Hesper on the iPhone (Connect a Mac › Scan the code). The iPhone that scans it is approved at once with the rights given, " +
			"without comparing codes. Each code is good for one iPhone and 5 minutes; a new one replaces it every 2 minutes. Exits when an iPhone has paired; " +
			"Ctrl-C cancels the open codes. Needs this Mac's host role; shell needs hesperd serve --allow-shell, and a device without hardware keys never gets it.",
		Output:   "one {payload, expires, id} per code, then {offer, device, name, rights} of the pairing",
		Examples: []string{"hesperctl phone", "hesperctl phone --rights observe,answer"},
		Run:      phoneCommand})
}

// phoneRenew is how often phone shows a new code.
var phoneRenew = 2 * time.Minute

func phoneCommand(ctx context.Context, f *flag.FlagSet, args []string) error {
	rights := f.String("rights", "", "Rights the iPhone gets: "+strings.Join(devicekey.AllRights, ",")+" (default "+strings.Join(host.DefaultOfferRights, ",")+")")
	socket := daemonSocket(f)
	asJSON := jsonFlag(f)
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() > 0 {
		return usagef("usage: hesperctl phone [--rights R,…] [--json]")
	}
	// The notifications come on a connection of their own: one that also
	// waits for results would stall while nobody reads them.
	events, err := subscribe(ctx, *socket)
	if err != nil {
		return err
	}
	defer events.Close()
	c, err := dialDaemon(ctx, *socket)
	if err != nil {
		return err
	}
	defer c.Close()
	var open []string
	defer func() {
		cancelCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		for _, id := range open {
			c.Call(cancelCtx, "devices.offerCancel", map[string]string{"id": id}, nil)
		}
	}()
	tty := isTerminal(os.Stdout)
	for {
		var offer host.OfferResult
		if err := c.Call(ctx, "devices.offer", host.OfferParams{Rights: parseRights(*rights)}, &offer); err != nil {
			return err
		}
		open = append(open, offer.ID)
		if err := showOffer(offer, *asJSON, tty); err != nil {
			return err
		}
		renew := time.NewTimer(renewAfter(offer))
	wait:
		for {
			select {
			case <-ctx.Done():
				renew.Stop()
				return errors.New("stopped before an iPhone paired")
			case <-renew.C:
				break wait
			case n, ok := <-events.Notifications():
				if !ok {
					return errDaemonGone
				}
				var p host.Paired
				if n.Method != host.NotePaired || json.Unmarshal(n.Params, &p) != nil || !slices.Contains(open, p.Offer) {
					continue
				}
				renew.Stop()
				open = slices.DeleteFunc(open, func(id string) bool { return id == p.Offer })
				if *asJSON {
					return json.NewEncoder(os.Stdout).Encode(p)
				}
				fmt.Printf("%s added with rights %s. Remove it: hesperctl devices-local --revoke %q\n", p.Name, strings.Join(p.Rights, ", "), p.Name)
				return nil
			}
		}
	}
}

// showOffer prints one code: a JSON line, or the QR code with what it
// grants (on a terminal in place of the one before).
func showOffer(offer host.OfferResult, asJSON, tty bool) error {
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(offer)
	}
	code, err := devicekey.ParsePairingCode(offer.Payload)
	if err != nil {
		return fmt.Errorf("hesperd sent an invalid pairing code: %w", err)
	}
	symbol, err := qr.Encode([]byte(offer.Payload), qr.L)
	if err != nil {
		return err
	}
	var b strings.Builder
	if tty {
		b.WriteString("\x1b[H\x1b[2J")
	}
	b.WriteString(symbol.HalfBlocks(4, tty))
	fmt.Fprintf(&b, "\nScan this with Hesper on your iPhone (Connect a Mac › Scan the code).\n")
	fmt.Fprintf(&b, "The iPhone gets %s on %s. New code at %s; Ctrl-C to stop.\n",
		strings.Join(code.Rights, ", "), code.Name, time.Now().Add(renewAfter(offer)).Format("15:04"))
	_, err = os.Stdout.WriteString(b.String())
	return err
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// renewAfter is when phone shows the next code: phoneRenew, or earlier
// when the offer expires sooner.
func renewAfter(offer host.OfferResult) time.Duration {
	return min(phoneRenew, time.Until(time.Unix(offer.Expires, 0))-30*time.Second)
}
