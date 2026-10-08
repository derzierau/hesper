// Package session defines shared session models and the host provider boundary. Provider IDs are opaque
// to clients and the relay; a new runtime identity invalidates old terminal targets.
package session

type Target struct {
	RuntimeID  string `json:"runtimeId"`
	TerminalID string `json:"terminalId"`
}

type Terminal struct {
	Target    Target `json:"target"`
	Role      string `json:"role"`
	State     string `json:"state"`
	Attention string `json:"attention"`
	// AttentionSince is the Unix second the current attention was raised. It
	// identifies that request: answers carry it so a newer prompt is never
	// answered by mistake.
	AttentionSince int64 `json:"attentionSince,omitempty"`
	Columns        int   `json:"columns"`
	Rows           int   `json:"rows"`
	Exited         bool  `json:"exited"`
}
type Capabilities struct {
	// Transfer accepts encrypted uploads (`transfer`, `probe`); Spawn also
	// starts agents from them (`spawn`, `job`, `stop`).
	Transfer bool `json:"transfer"`
	Spawn    bool `json:"spawn"`
	// Shell: the host runs with --allow-shell and accepts shell.open,
	// shell.close and shell.list (right "shell", strong signature).
	Shell bool `json:"shell,omitempty"`
	// E2E: the host answers the end-to-end channel (e2e.hello / e2e,
	// contract part N) with the static key E2EKey.
	E2E bool `json:"e2e,omitempty"`
	// Direct: the host listens for the direct path (docs/direct-path.md);
	// its addresses are given only inside the channel (direct.offer).
	Direct bool `json:"direct,omitempty"`
	// Ping: the host answers "ping" (unsigned, right observe, through the
	// channel when there is one) so controllers can time the round trip.
	Ping bool `json:"ping,omitempty"`
	// Agents: the host is a hesperd serving its agents (agents.* methods,
	// links; rebuild contract part R).
	Agents bool `json:"agents,omitempty"`
}
type Snapshot struct {
	RuntimeID    string       `json:"runtimeId"`
	Terminals    []Terminal   `json:"terminals"`
	Capabilities Capabilities `json:"capabilities"`
	// TransferKey is the host's X25519 public key (base64) for uploads.
	TransferKey string `json:"transferKey,omitempty"`
	// E2EKey is the host's static X25519 key for the end-to-end channel
	// (base64; the same key as TransferKey). Controllers pin it.
	E2EKey  string        `json:"e2eKey,omitempty"`
	Machine *MachineStats `json:"machine,omitempty"`
	// Short is a hesperd host's own short name for itself (agent ids
	// "<short>/<local id>"); controllers name it by machines.json first.
	Short string `json:"short,omitempty"`
}

// MachineStats describes the host machine for placement decisions. Values the
// host cannot determine are omitted. Fractions are 0..1.
type MachineStats struct {
	Agents     int      `json:"agents"`
	MemoryUsed *float64 `json:"memoryUsed,omitempty"`
	Battery    *float64 `json:"battery,omitempty"`
	OnBattery  *bool    `json:"onBattery,omitempty"`
	LidClosed  *bool    `json:"lidClosed,omitempty"`
}
