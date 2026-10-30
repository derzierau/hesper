package wire

// Forward is a port forward (forwards.*): connections to Local on this
// Mac's loopback reach Port on Machine's loopback, through the link to
// that machine's hesperd (which must run with --allow-forward).
type Forward struct {
	// ID is the local port in decimal; forwards.remove also takes
	// "machine:port" when only one forward goes there.
	ID      string `json:"id"`
	Machine string `json:"machine"`
	Port    int    `json:"port"`
	Local   int    `json:"local"`
	// Conns is the number of open connections.
	Conns int `json:"conns"`
	// Error is why the last connection was refused, or why this Mac
	// cannot listen on Local; empty once a connection got through.
	Error string `json:"error,omitempty"`
}

// ForwardAddParams are forwards.add's: Local 0 is Port, if free.
type ForwardAddParams struct {
	Machine string `json:"machine"`
	Port    int    `json:"port"`
	Local   int    `json:"local,omitempty"`
}
