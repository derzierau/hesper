package main

import (
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

func TestRelaySessionLine(t *testing.T) {
	if line := relaySessionLine(nil); line != "" {
		t.Fatalf("no drops: %q", line)
	}
	at := time.Date(2026, 11, 7, 14, 3, 9, 0, time.Local)
	got := relaySessionLine(&wire.RelaySession{Drops5m: 7, LastReason: "relay closed the connection: message too big", LastAt: at})
	if want := "relay session: dropped 7× in 5 min, last: relay closed the connection: message too big at 14:03:09"; got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	// The reason is the relay's text: control characters are not printed.
	got = relaySessionLine(&wire.RelaySession{Drops5m: 1, LastReason: "closed\x1b[2J\r\nnow\x07", LastAt: at})
	if want := "relay session: dropped 1× in 5 min, last: closed[2Jnow at 14:03:09"; got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	// Nor format characters such as bidi overrides, which reorder the line.
	got = relaySessionLine(&wire.RelaySession{Drops5m: 1, LastReason: "a‮b⁦c⁩d​", LastAt: at})
	if want := "relay session: dropped 1× in 5 min, last: abcd at 14:03:09"; got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	// No drop in the last five minutes: only the last one is told.
	got = relaySessionLine(&wire.RelaySession{LastReason: "relay closed the connection", LastAt: at})
	if want := "relay session: last dropped: relay closed the connection at 14:03:09"; got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}
