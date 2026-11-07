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
}
