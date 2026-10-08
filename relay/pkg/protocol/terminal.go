package protocol

// wire name: kept as "ghosty.terminal.v1" until the next relay deploy.
const TerminalProtocol = "ghosty.terminal.v1"
const TerminalChunk = 32 * 1024

// TerminalControl travels as text; application bytes always travel as binary.
type TerminalControl struct {
	Type    string `json:"type"`
	Columns uint16 `json:"columns,omitempty"`
	Rows    uint16 `json:"rows,omitempty"`
}
type TerminalTicket struct {
	ID      string `json:"id"`
	Columns uint16 `json:"columns"`
	Rows    uint16 `json:"rows"`
	// E2E is the stream's secret when terminal.open came through the
	// end-to-end channel (pkg/e2e Stream); the ticket then travels sealed.
	E2E []byte `json:"e2e,omitempty"`
	// Direct: terminal.open came on the direct path, and the stream is a
	// direct connection of its own (pkg/direct, Hello.Stream = ID), not a
	// relay socket. The host waits 15 seconds for it.
	Direct bool `json:"direct,omitempty"`
}
