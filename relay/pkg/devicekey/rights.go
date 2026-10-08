package devicekey

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"strings"
)

// Rights a host grants an approved controller.
const (
	Observe  = "observe"
	Answer   = "answer"
	Type     = "type"
	Transfer = "transfer"
	Shell    = "shell"
)

// AllRights in display order.
var AllRights = []string{Observe, Answer, Type, Transfer, Shell}

// ValidRight reports whether name is a known right.
func ValidRight(name string) bool {
	for _, r := range AllRights {
		if r == name {
			return true
		}
	}
	return false
}

// methodRights is the one table of which right each host method needs
// (contract Part K, as hesperd's agents methods since part R of the
// rebuild). agents.attach is decided by its mode, agents.spawn by whether
// it starts a shell.
var methodRights = map[string]string{
	"snapshot": Observe, "ping": Observe, "agents.list": Observe, "agents.link": Observe,
	"agents.plan": Observe, "agents.probe": Observe, "agents.screen": Observe, "agents.export": Observe, "download": Observe, "job": Observe,
	"agents.answer": Answer,
	"agents.input":  Type, "files.put": Type, "files.chunk": Type,
	"agents.spawn": Transfer, "agents.stop": Transfer, "agents.resume": Transfer, "agents.remove": Transfer,
	"agents.rename": Transfer, "agents.import": Transfer,
	// closing agents: ending an agent, or hiding it, as stop and remove.
	"agents.close": Transfer, "agents.kill": Transfer, "agents.background": Transfer, "transfer": Transfer, "projects.clone": Transfer,
	"projects.recent": Observe, "profiles.list": Observe,
	// A draft's folder on the machine it will start on (exists, isDir).
	"fs.stat": Observe,
	// projects step 1: reading the shared projects observes, sending
	// changes (projects.sync with a state) or promoting a folder needs
	// transfer.
	"projects.sync": Observe, "projects.promote": Transfer,
	// shared history: reading the index, transcripts, a session's plan
	// or changes observes; starting a session there needs transfer.
	"sessions.pull": Observe, "sessions.transcript": Observe, "sessions.plan": Observe, "sessions.changes": Observe,
	"sessions.resume": Transfer, "sessions.fork": Transfer, "sessions.continueAs": Transfer,
}

// Unsigned reports the methods controllers send without a signature and
// hosts answer for every routed controller: snapshot (relay-visible
// metadata only) and ping (an empty round trip the controller times; it
// reveals and changes nothing).
func Unsigned(method string) bool { return method == "snapshot" || method == "ping" }

// SpawnsShell reports whether agents.spawn params start a shell: kind
// "shell", or no kind and a profile named like one. Hosts decide by the
// profile they resolve and refuse a shell the params did not announce.
func SpawnsShell(params []byte) bool {
	var p struct {
		Kind    string `json:"kind"`
		Profile string `json:"profile"`
	}
	if json.Unmarshal(params, &p) != nil {
		return false
	}
	return p.Kind == Shell || p.Kind == "" && strings.Contains(p.Profile, Shell)
}

// MethodRight returns the right a request needs and whether it must be
// signed with the strong key. ok is false for a method that is not in the
// table; hosts refuse those when they enforce device keys.
func MethodRight(method string, params []byte) (right string, strong, ok bool) {
	switch method {
	case "agents.attach":
		// A writable attach types; only an explicit mode "ro" observes.
		// Params that do not parse ask for the larger right.
		var p struct {
			Mode string `json:"mode"`
		}
		if json.Unmarshal(params, &p) == nil && p.Mode == "ro" {
			return Observe, false, true
		}
		return Type, false, true
	case "agents.spawn":
		// A shell on another machine: the shell right and Touch ID.
		if SpawnsShell(params) {
			return Shell, true, true
		}
	case "projects.sync":
		// Sending a state changes the host's projects.
		var p struct {
			State json.RawMessage `json:"state"`
		}
		if json.Unmarshal(params, &p) != nil || len(p.State) > 0 && string(p.State) != "null" {
			return Transfer, false, true
		}
	}
	right, ok = methodRights[method]
	return right, false, ok
}

// NeedsStrong reports whether a request must be signed with the strong key.
func NeedsStrong(method string, params []byte) bool {
	_, strong, _ := MethodRight(method, params)
	return strong
}

// ApprovalCode is the 6-character code both sides show while a controller
// asks a host for approval: the first 30 bits of SHA-256(host key ||
// device key || strong key), base32, as "ABC-DEF". hostKey is the host's
// X25519 transfer public key (32 bytes), the device keys are DER
// SubjectPublicKeyInfo (91 bytes each), so the concatenation is unambiguous.
// A relay that substitutes any of the keys changes the code on one side.
func ApprovalCode(hostKey, deviceKey, strongKey []byte) string {
	h := sha256.New()
	h.Write(hostKey)
	h.Write(deviceKey)
	h.Write(strongKey)
	code := base32.StdEncoding.EncodeToString(h.Sum(nil))[:6]
	return code[:3] + "-" + code[3:]
}

// NormalizeCode turns user input ("abc def", "ABC-DEF") into "ABC-DEF", or ""
// when it is not a code.
func NormalizeCode(value string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(value) {
		switch {
		case r == '-' || r == ' ':
		case (r >= 'A' && r <= 'Z') || (r >= '2' && r <= '7'):
			b.WriteRune(r)
		default:
			return ""
		}
	}
	code := b.String()
	if len(code) != 6 {
		return ""
	}
	return code[:3] + "-" + code[3:]
}
