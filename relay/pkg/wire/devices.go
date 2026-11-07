package wire

// Removing a device (devices.list, devices.remove on the local socket):
// every device of the owner, merged from the relay's list and this Mac's
// files, and the one-step removal from all of them.

// Device kinds: an iPhone is a controller with an APNs registration on
// the relay; everything else is a Mac.
const (
	DeviceMac    = "mac"
	DeviceIPhone = "iphone"
)

// CodeUnsupported: the relay is too old for the request (devices.remove
// without the relay's "devices" feature).
const CodeUnsupported = "unsupported"

// Outcomes of one removal step (DeviceRemoval); anything else is the
// step's error text.
const (
	StepDone    = "done"
	StepSkipped = "skipped"
)

// Device is one entry of devices.list.
type Device struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Short string `json:"short,omitempty"` // machines.json
	Role  string `json:"role"`            // "host" or "controller"
	Kind  string `json:"kind"`            // DeviceMac or DeviceIPhone
	// Online: it has a live relay connection (false when the relay is
	// not reachable or does not list it).
	Online bool `json:"online"`
	// Self: one of this Mac's own two ids (host or controller).
	Self bool `json:"self"`
	// ApprovedHere: in this host's controllers.json; TrustedHere: pinned
	// in this Mac's trusted-hosts.json; Relay: the relay lists it.
	ApprovedHere bool `json:"approvedHere"`
	TrustedHere  bool `json:"trustedHere"`
	Relay        bool `json:"relay"`
}

// DevicesList is the result of devices.list.
type DevicesList struct {
	Devices []Device `json:"devices"`
}

// DeviceRemoveParams are devices.remove's params. LocalOnly skips the
// relay (for a relay too old to revoke).
type DeviceRemoveParams struct {
	DeviceID  string `json:"deviceId"`
	LocalOnly bool   `json:"localOnly,omitempty"`
}

// DeviceRemoval is the result of devices.remove: each step StepDone,
// StepSkipped or its error. SignedOut are the offline controller records
// of the same name that a host's removal also revoked.
type DeviceRemoval struct {
	Relay     string   `json:"relay"`
	Approval  string   `json:"approval"`
	Trust     string   `json:"trust"`
	Names     string   `json:"names"`
	SignedOut []string `json:"signedOut,omitempty"`
}
