package agents

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/derzierau/hesper/relay/pkg/client"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// BuiltinProfiles are the launch profiles without a profiles.json.
func BuiltinProfiles() map[string]wire.Profile {
	return map[string]wire.Profile{
		"claude-auto-rc": {Kind: wire.KindClaude, Argv: []string{"claude", "--permission-mode", "auto", "--remote-control", "{name}"}},
		"claude-bypass":  {Kind: wire.KindClaude, Argv: []string{"claude", "--dangerously-skip-permissions", "--remote-control", "{name}"}},
		"codex-full":     {Kind: wire.KindCodex, Argv: []string{"codex", "--dangerously-bypass-approvals-and-sandbox"}},
		"shell":          {Kind: wire.KindShell, Argv: []string{"{loginShell}", "-l"}},
	}
}

// Settings is settings.json in the config directory.
type Settings struct {
	// Machine is this Mac's short name (agent ids start with it).
	Machine string `json:"machine,omitempty"`
	// Defaults: the default kind, the profile per kind and per project.
	Defaults wire.Defaults `json:"defaults"`
	// Size of a new agent's PTY until a view owns it.
	Size *wire.Size `json:"size,omitempty"`
	// TrustProjects (default true): on spawn, record the project as
	// trusted in Claude Code's ~/.claude.json / Codex's config.toml, as
	// accepting their trust question would (trust.go). false: the agent
	// asks, and the daemon shows the question as attention.
	TrustProjects *bool `json:"trustProjects,omitempty"`
	// CodexSessionHooks (default true): Codex agents get hesperd's hooks
	// and notify as -c overrides on their command line (codexargs.go), so
	// nothing in ~/.codex has to change. Skipped when hooks.json already
	// has hesperd's hooks.
	CodexSessionHooks *bool `json:"codexSessionHooks,omitempty"`
	// CodexUpdatePrompt: Codex's "Update available" screen at start.
	// "ask" (default): shown as a question (skip | update); "skip": Codex
	// agents get -c check_for_update_on_startup=false, and the screen, if
	// it still shows, is skipped.
	CodexUpdatePrompt string `json:"codexUpdatePrompt,omitempty"`
	// Attachments: where files dropped on an agent are stored on its
	// machine (files.put). "state" (default): <state dir>/attachments/
	// <local id>/, so nothing lands in a repository; "project": the
	// agent's worktree or project, .hesper/attachments/.
	Attachments string `json:"attachments,omitempty"`
	// MaxAgentDepth and MaxAgentChildren (agent tree, tree.go) bound the
	// agents agents start: how deep below a person's agent (default 3),
	// and how many live children one agent may have (default 8). 0 means
	// agents may start none.
	MaxAgentDepth    *int `json:"maxAgentDepth,omitempty"`
	MaxAgentChildren *int `json:"maxAgentChildren,omitempty"`
}

// codexUpdatePrompt values.
const (
	CodexUpdateAsk  = "ask"
	CodexUpdateSkip = "skip"
)

func (s Settings) trustProjects() bool     { return s.TrustProjects == nil || *s.TrustProjects }
func (s Settings) codexSessionHooks() bool { return s.CodexSessionHooks == nil || *s.CodexSessionHooks }
func (s Settings) codexUpdatePrompt() string {
	if s.CodexUpdatePrompt == CodexUpdateSkip {
		return CodexUpdateSkip
	}
	return CodexUpdateAsk
}

func defaultSettings() Settings {
	return Settings{Defaults: wire.Defaults{
		Kind:     wire.KindClaude,
		Kinds:    map[string]string{wire.KindClaude: "claude-auto-rc", wire.KindCodex: "codex-full", wire.KindShell: "shell"},
		Projects: map[string]string{},
	}}
}

// loadProfiles merges profiles.json over the built-in profiles.
func loadProfiles(dir string) (map[string]wire.Profile, error) {
	profiles := BuiltinProfiles()
	data, err := os.ReadFile(filepath.Join(dir, "profiles.json"))
	if errors.Is(err, os.ErrNotExist) {
		return profiles, nil
	}
	if err != nil {
		return profiles, err
	}
	var user map[string]wire.Profile
	if err := json.Unmarshal(data, &user); err != nil {
		return profiles, fmt.Errorf("profiles.json: %w", err)
	}
	for name, p := range user {
		if len(p.Argv) == 0 {
			delete(profiles, name) // an empty profile removes a built-in one
			continue
		}
		if !validKind(p.Kind) {
			return profiles, fmt.Errorf("profiles.json: %s: kind must be claude, codex or shell", name)
		}
		profiles[name] = p
	}
	return profiles, nil
}

func validKind(k string) bool {
	return k == wire.KindClaude || k == wire.KindCodex || k == wire.KindShell
}

// loadSettings reads settings.json over the defaults.
func loadSettings(dir string) (Settings, error) {
	s := defaultSettings()
	data, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	var user Settings
	if err := json.Unmarshal(data, &user); err != nil {
		return s, fmt.Errorf("settings.json: %w", err)
	}
	s.Machine, s.Size = user.Machine, user.Size
	s.TrustProjects, s.CodexSessionHooks = user.TrustProjects, user.CodexSessionHooks
	s.MaxAgentDepth, s.MaxAgentChildren = user.MaxAgentDepth, user.MaxAgentChildren
	switch user.CodexUpdatePrompt {
	case "", CodexUpdateAsk, CodexUpdateSkip:
		s.CodexUpdatePrompt = user.CodexUpdatePrompt
	default:
		return s, fmt.Errorf("settings.json: codexUpdatePrompt must be %q or %q", CodexUpdateAsk, CodexUpdateSkip)
	}
	switch user.Attachments {
	case "", AttachmentsState, AttachmentsProject:
		s.Attachments = user.Attachments
	default:
		return s, fmt.Errorf("settings.json: attachments must be %q or %q", AttachmentsState, AttachmentsProject)
	}
	if user.Defaults.Kind != "" {
		s.Defaults.Kind = user.Defaults.Kind
	}
	maps.Copy(s.Defaults.Kinds, user.Defaults.Kinds)
	for path, profile := range user.Defaults.Projects {
		s.Defaults.Projects[cleanPath(path)] = profile
	}
	return s, nil
}

// resolveProfile picks the profile of a spawn: the one named, else the
// project's default when it has the kind asked for (or none was), else the
// kind's default (the default kind's without a kind).
func resolveProfile(profiles map[string]wire.Profile, d wire.Defaults, name, kind, project string) (string, wire.Profile, error) {
	if name != "" {
		p, ok := profiles[name]
		if !ok {
			return "", p, wire.Errorf(wire.CodeNotFound, "no profile %q", name)
		}
		if kind != "" && p.Kind != kind {
			return "", p, wire.Errorf(wire.CodeInvalid, "profile %q is %s, not %s", name, p.Kind, kind)
		}
		return name, p, nil
	}
	if kind != "" && !validKind(kind) {
		return "", wire.Profile{}, wire.Errorf(wire.CodeInvalid, "kind must be claude, codex or shell")
	}
	if pn, ok := d.Projects[cleanPath(project)]; ok {
		if p, ok := profiles[pn]; ok && (kind == "" || p.Kind == kind) {
			return pn, p, nil
		}
	}
	if kind == "" {
		kind = d.Kind
	}
	pn := d.Kinds[kind]
	if p, ok := profiles[pn]; ok && p.Kind == kind {
		return pn, p, nil
	}
	for name, p := range profiles {
		if p.Kind == kind {
			return name, p, nil
		}
	}
	return "", wire.Profile{}, wire.Errorf(wire.CodeNotFound, "no profile for %s", kind)
}

func cleanPath(p string) string {
	if p == "" {
		return ""
	}
	return filepath.Clean(expandHome(p))
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[1:])
	}
	return p
}

// DefaultMachine is this Mac's short name when nothing names it: host
// names are often an asset tag ("ABC123456"), and the short name is in
// every agent id. settings.json "machine" sets a real one.
const DefaultMachine = "L"

// machineShort is this Mac's short name: settings, HESPER_MACHINE,
// machines.json's entry for this host's device, else DefaultMachine.
func machineShort(settings Settings, stateDir, configDir string) string {
	if settings.Machine != "" {
		return settings.Machine
	}
	if m := os.Getenv("HESPER_MACHINE"); m != "" {
		return m
	}
	if id := hostDeviceID(filepath.Join(stateDir, "host.credentials.json")); id != "" {
		var config struct {
			Machines map[string]struct {
				Short string `json:"short"`
			} `json:"machines"`
		}
		if data, err := os.ReadFile(filepath.Join(configDir, "machines.json")); err == nil && json.Unmarshal(data, &config) == nil {
			if s := config.Machines[id].Short; s != "" {
				return s
			}
		}
	}
	return DefaultMachine
}

func hostDeviceID(path string) string {
	c, err := client.LoadCredentials(path)
	if err != nil {
		return ""
	}
	return c.DeviceID
}
