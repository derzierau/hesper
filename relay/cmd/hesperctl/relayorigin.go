package main

import (
	"github.com/derzierau/hesper/relay/pkg/client"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// relayHelp explains where login and pair find the relay.
const relayHelp = "The relay is one you deploy yourself (relay/docs/deployment.md); Hesper has none built in. Its origin comes from --relay, else $HESPER_RELAY, else \"relay\" in ~/.config/hesper/settings.json, else the relay this Mac's host or controller credentials were issued by."

// resolveRelay is the relay origin for login and pair (client.ResolveRelay
// over the config and state directories).
func resolveRelay(flag string) (string, error) {
	origin, _, err := client.ResolveRelay(flag, wire.ConfigDir(), wire.StateDir())
	return origin, err
}
