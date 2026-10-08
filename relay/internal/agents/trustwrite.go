package agents

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"
)

// Pre-trust: a project the user chose for a new agent is recorded as
// trusted where the agent itself records it when the user accepts its
// trust question (as the former bin/ghosty-handoff trust did), so the
// agent starts working instead of waiting on a question nobody sees.
// settings.json "trustProjects": false turns it off.
//
//   - Claude Code: ~/.claude.json (or $CLAUDE_CONFIG_DIR/.claude.json)
//     projects[<key>].hasTrustDialogAccepted = true, the entry otherwise
//     as Claude Code creates it. Never the home folder or above (Claude
//     never saves that either), and only when the file exists (Claude
//     creates it on its first run).
//   - Codex: $CODEX_HOME/config.toml [projects."<key>"] trust_level =
//     "trusted", unless the project has a trust_level already (an explicit
//     "untrusted" stays).
//
// The key is the repository's main worktree root (both agents trust a
// whole repository: "trusting it trusts that whole repository"), else the
// directory itself, symlinks resolved.
//
// Both files are written by their agents at any time (Claude Code rewrites
// ~/.claude.json constantly), so a change is a read-modify-write that
// never clobbers: under Claude Code's own lock (proper-lockfile's
// "<file>.lock" directory, so a Claude Code that honours it waits for
// us), the new content goes to a temp file next to the target, the target
// is read again just before the rename and the whole edit starts over if
// it changed meanwhile. The result must parse to the old content plus our
// one change, or nothing is written.

// Results of a pre-trust.
const (
	trustAlready = "already" // trusted before
	trustMarked  = "marked"  // recorded now
	trustKept    = "kept"    // Codex: the user decided otherwise here
	trustMissing = "missing" // Claude Code has not run on this Mac yet
	trustRefused = "refused" // the home folder or above
)

// trustKey is where an agent records trust for dir.
func trustKey(dir string, g git) string {
	if root := g.mainRoot(dir); root != "" {
		if real, err := filepath.EvalSymlinks(root); err == nil {
			return real
		}
		return root
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		return real
	}
	return filepath.Clean(dir)
}

// homeOrAbove: trusting path would trust the whole home folder.
func homeOrAbove(path, home string) bool {
	if path == "" || path == "/" {
		return true
	}
	if real, err := filepath.EvalSymlinks(home); err == nil {
		home = real
	}
	return home == path || strings.HasPrefix(home, strings.TrimRight(path, "/")+"/")
}

// pretrust records dir as trusted for the agent kind (claude or codex).
func (r *Registry) pretrust(kind, dir string) (string, error) {
	switch kind {
	case "claude":
		if claudeTrusted(r.opt.ClaudeConfig, dir, r.g) {
			return trustAlready, nil
		}
		key := trustKey(dir, r.g)
		if homeOrAbove(key, r.opt.Home) {
			return trustRefused, nil
		}
		return trustClaude(r.opt.ClaudeConfig, key)
	case "codex":
		key := trustKey(dir, r.g)
		if homeOrAbove(key, r.opt.Home) {
			return trustRefused, nil
		}
		return trustCodex(filepath.Join(r.opt.CodexHome, "config.toml"), key)
	}
	return "", nil
}

// claudeProjectDefaults is the entry Claude Code creates for a project.
var claudeProjectDefaults = []byte(`{"allowedTools":[],"mcpContextUris":[],"mcpServers":{},"enabledMcpjsonServers":[],"disabledMcpjsonServers":[],"hasTrustDialogAccepted":false,"projectOnboardingSeenCount":0,"hasClaudeMdExternalIncludesApproved":false,"hasClaudeMdExternalIncludesWarningShown":false}`)

// errNoChange ends an edit that has nothing to write.
var errNoChange = errors.New("no change")

// trustClaude sets projects[key].hasTrustDialogAccepted in config.
func trustClaude(config, key string) (string, error) {
	if _, err := os.Stat(config); errors.Is(err, os.ErrNotExist) {
		return trustMissing, nil
	}
	result := trustMarked
	err := updateFile(config, true, func(old []byte) ([]byte, error) {
		out, already, err := claudeTrustEdit(old, key)
		if err != nil {
			return nil, err
		}
		if already {
			result = trustAlready
			return nil, errNoChange
		}
		result = trustMarked
		return out, nil
	})
	if errors.Is(err, errNoChange) {
		err = nil
	}
	return result, err
}

// claudeTrustEdit is ~/.claude.json with projects[key] trusted: every key
// in its order, every other value byte for byte (re-indented the way
// Claude Code writes it, two spaces).
func claudeTrustEdit(old []byte, key string) ([]byte, bool, error) {
	top, err := orderedObject(old)
	if err != nil {
		return nil, false, fmt.Errorf("~/.claude.json: %w", err)
	}
	projectsRaw, _ := top.get("projects")
	var projects jsonObject
	if projectsRaw != nil && string(bytes.TrimSpace(projectsRaw)) != "null" {
		if projects, err = orderedObject(projectsRaw); err != nil {
			return nil, false, fmt.Errorf("~/.claude.json projects: %w", err)
		}
	}
	entryRaw, ok := projects.get(key)
	var entry jsonObject
	if ok {
		if entry, err = orderedObject(entryRaw); err != nil {
			return nil, false, fmt.Errorf("~/.claude.json projects[%q]: %w", key, err)
		}
		if v, _ := entry.get("hasTrustDialogAccepted"); string(bytes.TrimSpace(v)) == "true" {
			return nil, true, nil
		}
	} else {
		entry, _ = orderedObject(claudeProjectDefaults)
	}
	entry.set("hasTrustDialogAccepted", json.RawMessage("true"))
	projects.set(key, entry.encode())
	top.set("projects", projects.encode())
	var out bytes.Buffer
	if err := json.Indent(&out, top.encode(), "", "  "); err != nil {
		return nil, false, err
	}
	if bytes.HasSuffix(bytes.TrimRight(old, " \t\r"), []byte("\n")) || len(bytes.TrimSpace(old)) == 0 {
		out.WriteByte('\n')
	}
	// The new file must say what the old one said, plus the trust.
	var before, after map[string]any
	if err := unmarshalNumbers(old, &before); err != nil {
		return nil, false, err
	}
	if err := unmarshalNumbers(out.Bytes(), &after); err != nil {
		return nil, false, err
	}
	want, _ := before["projects"].(map[string]any)
	if want == nil {
		want = map[string]any{}
	}
	e, _ := want[key].(map[string]any)
	if e == nil {
		var d map[string]any
		unmarshalNumbers(claudeProjectDefaults, &d)
		e = d
	}
	e["hasTrustDialogAccepted"] = true
	want[key] = e
	before["projects"] = want
	if !reflect.DeepEqual(before, after) {
		return nil, false, errors.New("~/.claude.json: the edit would change more than the trust")
	}
	return out.Bytes(), false, nil
}

func unmarshalNumbers(data []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	return d.Decode(v)
}

// jsonObject is a JSON object's members in their order, values raw.
type jsonObject []jsonMember

type jsonMember struct {
	key   string
	value json.RawMessage
}

func orderedObject(data []byte) (jsonObject, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	if t != json.Delim('{') {
		return nil, errors.New("not a JSON object")
	}
	var o jsonObject
	for d.More() {
		t, err := d.Token()
		if err != nil {
			return nil, err
		}
		k, _ := t.(string)
		var v json.RawMessage
		if err := d.Decode(&v); err != nil {
			return nil, err
		}
		o = append(o, jsonMember{k, v})
	}
	if _, err := d.Token(); err != nil {
		return nil, err
	}
	if _, err := d.Token(); err == nil {
		return nil, errors.New("data after the object")
	}
	return o, nil
}

func (o jsonObject) get(k string) (json.RawMessage, bool) {
	for i := len(o) - 1; i >= 0; i-- {
		if o[i].key == k {
			return o[i].value, true
		}
	}
	return nil, false
}

func (o *jsonObject) set(k string, v json.RawMessage) {
	for i := range *o {
		if (*o)[i].key == k {
			(*o)[i].value = v
			return
		}
	}
	*o = append(*o, jsonMember{k, v})
}

func (o jsonObject) encode() json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(jsonString(m.key))
		b.WriteByte(':')
		json.Compact(&b, m.value)
	}
	b.WriteByte('}')
	return b.Bytes()
}

// jsonString encodes s as JSON does in JavaScript: no HTML escapes.
func jsonString(s string) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(s)
	return bytes.TrimRight(b.Bytes(), "\n")
}

// trustCodex sets [projects."key"] trust_level = "trusted" in config.toml.
func trustCodex(config, key string) (string, error) {
	result := trustMarked
	err := updateFile(config, false, func(old []byte) ([]byte, error) {
		out, res, err := codexTrustEdit(string(old), key)
		if err != nil {
			return nil, err
		}
		result = res
		if res != trustMarked {
			return nil, errNoChange
		}
		return []byte(out), nil
	})
	if errors.Is(err, errNoChange) {
		err = nil
	}
	return result, err
}

// codexTrustLevel is the trust_level a config.toml gives key ("" none):
// the project's table, or a dotted key at the top level.
func codexTrustLevel(text, key string) (string, bool) {
	start, end, found := codexProjectTable(text, key)
	if found {
		if m := trustLevelLine.FindStringSubmatch(text[start:end]); m != nil {
			return m[1], true
		}
		return "", true
	}
	return "", false
}

var (
	trustLevelLine = regexp.MustCompile(`(?m)^[ \t]*trust_level[ \t]*=[ \t]*["']([a-z_-]*)["'][ \t]*(?:#.*)?$`)
	tableHeader    = regexp.MustCompile(`(?m)^[ \t]*\[\[?[^\]\n]*\]\]?[ \t]*(?:#.*)?$`)
	// projectsElsewhere: projects written other than as [projects."…"]
	// tables (an inline table, dotted keys): not edited by hand here.
	projectsElsewhere = regexp.MustCompile(`(?m)^[ \t]*projects[ \t]*(=|\.)`)
)

// codexProjectTable finds [projects."key"]: from after its header to the
// next table header.
func codexProjectTable(text, key string) (start, end int, found bool) {
	header := regexp.MustCompile(`(?m)^[ \t]*\[[ \t]*projects[ \t]*\.[ \t]*(?:` + regexp.QuoteMeta(tomlString(key)) +
		`|'` + regexp.QuoteMeta(key) + `')[ \t]*\][ \t]*(?:#.*)?$`)
	loc := header.FindStringIndex(text)
	if loc == nil {
		return 0, 0, false
	}
	start = loc[1]
	if start < len(text) && text[start] == '\n' {
		start++
	}
	end = len(text)
	if next := tableHeader.FindStringIndex(text[start:]); next != nil {
		end = start + next[0]
	}
	return start, end, true
}

// codexTrustEdit is config.toml with the project trusted: a line added to
// its table, else a new table at the end; the rest byte for byte.
func codexTrustEdit(text, key string) (string, string, error) {
	if level, found := codexTrustLevel(text, key); found && level != "" {
		if level == "trusted" {
			return text, trustAlready, nil
		}
		return text, trustKept, nil
	}
	line := "trust_level = \"trusted\"\n"
	base := text
	if base != "" && !strings.HasSuffix(base, "\n") {
		base += "\n"
	}
	var out string
	if start, _, found := codexProjectTable(base, key); found {
		out = base[:start] + line + base[start:]
	} else {
		if projectsElsewhere.MatchString(text) {
			return "", "", errors.New("config.toml: projects are not written as [projects.\"…\"] tables; not edited")
		}
		sep := ""
		if strings.TrimSpace(base) != "" {
			sep = "\n"
		}
		out = base + sep + "[projects." + tomlString(key) + "]\n" + line
	}
	if level, found := codexTrustLevel(out, key); !found || level != "trusted" {
		return "", "", errors.New("config.toml: the edit did not take")
	}
	return out, trustMarked, nil
}

// tomlString is a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, "\\u%04x", r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// codexTrusted reports whether Codex's config.toml trusts dir (its key).
func codexTrusted(config, dir string, g git) bool {
	data, err := os.ReadFile(config)
	if err != nil {
		return false
	}
	level, _ := codexTrustLevel(string(data), trustKey(dir, g))
	return level == "trusted"
}

// Locking of files other programs write.
var (
	lockWait  = 3 * time.Second  // how long to wait for another writer's lock
	lockStale = 10 * time.Second // proper-lockfile's default: an older lock is abandoned
	editTries = 8                // edits started over when the file changed under us
)

// updateFile applies edit to path's content: under path.lock (with lock,
// the directory proper-lockfile uses), written to a temp file, renamed
// over path only if path still holds what edit saw (else edit runs again).
// A symlink is followed; the file keeps its mode. edit returning
// errNoChange ends it without writing.
func updateFile(path string, lock bool, edit func(old []byte) ([]byte, error)) error {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	if lock {
		unlock, err := lockDir(path + ".lock")
		if err != nil {
			return err
		}
		defer unlock()
	}
	for range editTries {
		old, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		mode := os.FileMode(0o600)
		if st, err := os.Stat(path); err == nil {
			mode = st.Mode().Perm()
		}
		out, err := edit(old)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".hesperd-*")
		if err != nil {
			return err
		}
		_, werr := tmp.Write(out)
		if werr == nil {
			werr = tmp.Chmod(mode)
		}
		if werr == nil {
			werr = tmp.Sync()
		}
		if cerr := tmp.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			os.Remove(tmp.Name())
			return werr
		}
		now, err := os.ReadFile(path)
		if (err == nil && !bytes.Equal(now, old)) || (err != nil && old != nil) {
			os.Remove(tmp.Name()) // someone wrote meanwhile: start over on theirs
			time.Sleep(20 * time.Millisecond)
			continue
		}
		if err := os.Rename(tmp.Name(), path); err != nil {
			os.Remove(tmp.Name())
			return err
		}
		return nil
	}
	return fmt.Errorf("%s keeps changing; not written", path)
}

// lockDir takes a proper-lockfile style lock (a directory) and returns its
// release.
func lockDir(dir string) (func(), error) {
	deadline := time.Now().Add(lockWait)
	for {
		err := os.Mkdir(dir, 0o700)
		if err == nil {
			return func() { os.Remove(dir) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if st, err := os.Stat(dir); err == nil && time.Since(st.ModTime()) > lockStale {
			os.Remove(dir) // its holder is gone
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%s is held by another program", dir)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
