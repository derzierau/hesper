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
	// firewall is off, the executable is explicitly allowed, or a verified
	// Developer ID signature qualifies for automatic downloaded-app allowance).
	Allowed bool
	// Reason explains a verdict that is not Allowed.
	Reason string
}

// MacFirewall asks socketfilterfw (read-only queries, no root needed) about
// exe. Unknown executables are allowed only when macOS automatically allows
// downloaded signed software and their Developer ID signature verifies.
// Otherwise they could raise a firewall dialog from the background daemon.
// run executes the tool (nil: exec); tests replace it.
func MacFirewall(exe string, run func(args ...string) string) FirewallVerdict {
	return macFirewall(exe, run, func(exe string) bool {
		return developerIDSigned(context.Background(), exe)
	})
}

func developerIDSigned(ctx context.Context, exe string) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "/usr/bin/codesign", "--verify", "--strict", "-R=anchor apple generic and certificate leaf[field.1.2.840.113635.100.6.1.13] exists", exe).Run() == nil
}

func macFirewall(exe string, run func(args ...string) string, signed func(string) bool) FirewallVerdict {
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
	for _, line := range strings.Split(run("--getallowsigned"), "\n") {
		if strings.TrimSpace(line) == "Automatically allow downloaded signed software ENABLED." && signed(exe) {
			return FirewallVerdict{Allowed: true}
		}
	}
	return FirewallVerdict{Reason: "the firewall does not know " + exe + " yet (macOS would ask at the Mac)"}
}

// FirewallAdvice is the one-time fix for MacFirewall's refusals.
func FirewallAdvice(exe string) string {
	return "sudo " + SocketFilter + " --add " + exe + " && sudo " + SocketFilter + " --unblockapp " + exe + "  (again after each update of hesperd; then restart it), or start hesperd serve with --direct=on"
}
