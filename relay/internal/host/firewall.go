package host

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// SocketFilter is macOS's application firewall tool.
const SocketFilter = "/usr/libexec/ApplicationFirewall/socketfilterfw"

// FirewallVerdict is what the macOS application firewall will do with
// incoming connections to an executable.
type FirewallVerdict struct {
	// Allowed: connections reach it without anyone being asked (the
	// firewall is off, or the executable is explicitly allowed).
	Allowed bool
	// Reason explains a verdict that is not Allowed.
	Reason string
}

// MacFirewall asks socketfilterfw (read-only queries, no root needed) about
// exe. An executable the firewall does not know yet would make macOS ask
// the user at the Mac whether it may accept incoming connections, and a
// background hesperd should not raise that dialog; it counts as not
// allowed. run executes the tool (nil: exec); tests replace it.
func MacFirewall(exe string, run func(args ...string) string) FirewallVerdict {
	if run == nil {
		run = func(args ...string) string {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			out, _ := exec.CommandContext(ctx, SocketFilter, args...).CombinedOutput()
			return string(out)
		}
	}
	state := run("--getglobalstate")
	switch {
	case strings.Contains(state, "disabled") || strings.Contains(state, "State = 0"):
		return FirewallVerdict{Allowed: true}
	case !strings.Contains(state, "enabled"):
		// No answer (not macOS, tool missing): nothing filters.
		return FirewallVerdict{Allowed: true}
	}
	if blockAll := run("--getblockall"); strings.Contains(blockAll, "set to enabled") || strings.Contains(blockAll, "ENABLED") {
		return FirewallVerdict{Reason: "the firewall blocks all incoming connections"}
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	lines := strings.Split(run("--listapps"), "\n")
	for i, line := range lines {
		_, path, ok := strings.Cut(line, " : ")
		if !ok || strings.TrimSpace(path) != exe {
			continue
		}
		if i+1 < len(lines) && strings.Contains(lines[i+1], "Allow") {
			return FirewallVerdict{Allowed: true}
		}
		return FirewallVerdict{Reason: "the firewall blocks incoming connections to " + exe}
	}
	return FirewallVerdict{Reason: "the firewall does not know " + exe + " yet (macOS would ask at the Mac)"}
}

// FirewallAdvice is the one-time fix for MacFirewall's refusals.
func FirewallAdvice(exe string) string {
	return "sudo " + SocketFilter + " --add " + exe + " && sudo " + SocketFilter + " --unblockapp " + exe + "  (again after each update of hesperd; then restart it), or start hesperd serve with --direct=on"
}
