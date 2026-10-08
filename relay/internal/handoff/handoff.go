// Package handoff moves an agent with its conversation and its code to
// another machine (agents.move, rebuild contract part R). It is the Go
// port of what the former bin/ghosty-handoff's pack, have and unpack did for a move:
//
//   - the source packs a bundle directory: manifest.json, the agent's
//     conversation (Claude's session file or Codex's rollout) as
//     transcript.jsonl, and code.bundle, a Git bundle of its branch plus
//     its uncommitted work as a handoff commit (base <- staged <- all
//     files), incremental from commits the target already has;
//   - the target unpacks it: the repository (created from a full bundle
//     when missing), the branch at the base commit (in the same worktree
//     path, mapped to this machine's home), the uncommitted work restored
//     as uncommitted changes, and the conversation placed where the agent
//     CLI looks for it (paths in it mapped to this home), so the agent
//     resumes with --resume / codex resume.
//
// The files travel with the transfer crypto (pkg/transfer) inside the
// end-to-end channel; this package only reads and writes directories.
package handoff

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Version of manifest.json.
const Version = 2

// Files of a bundle.
const (
	ManifestFile   = "manifest.json"
	TranscriptFile = "transcript.jsonl"
	BundleFile     = "code.bundle"
)

var (
	idRE      = regexp.MustCompile(`^ho-[0-9a-f]{12}$`)
	sessionRE = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	localRE   = regexp.MustCompile(`^[a-z0-9]{1,32}$`)
)

// Paths are a machine's homes: its user home (paths under the source's
// home map to it), Claude's (~/.claude) and Codex's (~/.codex) data, and
// the environment git runs with (nil: the process's).
type Paths struct {
	Home, ClaudeHome, CodexHome string
	Env                         []string
}

// Manifest describes a bundle.
type Manifest struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
	Created int64  `json:"created"`
	Source  struct {
		Machine string `json:"machine"`
		Home    string `json:"home"`
	} `json:"source"`
	Agent   AgentInfo   `json:"agent"`
	Project ProjectInfo `json:"project"`
}

// AgentInfo is the moved agent.
type AgentInfo struct {
	LocalID   string    `json:"localId"`
	Kind      string    `json:"kind"`
	Profile   string    `json:"profile"`
	Name      string    `json:"name"`
	Task      string    `json:"task"`
	Created   time.Time `json:"created"`
	SessionID string    `json:"sessionId,omitempty"`
	// Transcript is set when transcript.jsonl travels: for Codex its path
	// below sessions/ (the rollout's date folders), for Claude the file
	// name.
	Transcript string `json:"transcript,omitempty"`
	// Parent, Depth, LetParentAnswer (agent tree): the moved agent keeps
	// its place in the tree (the parent's id as the source named it).
	Parent          string `json:"parent,omitempty"`
	Depth           int    `json:"depth,omitempty"`
	LetParentAnswer bool   `json:"letParentAnswer,omitempty"`
}

// ProjectInfo is where its code is.
type ProjectInfo struct {
	Path       string `json:"path"`
	Worktree   string `json:"worktree,omitempty"`
	Branch     string `json:"branch,omitempty"`
	MainBranch string `json:"mainBranch,omitempty"`
	Base       string `json:"base,omitempty"`
	Handoff    string `json:"handoff,omitempty"`
	Remote     string `json:"remote,omitempty"`
	// Bundle: "full", "incremental" or "" (no code.bundle: not a Git
	// repository, or one without a commit).
	Bundle string `json:"bundle,omitempty"`
	Have   string `json:"have,omitempty"`
}

// Plan is what the source tells the controller before a move: the
// project (as on the source) and the commits the target may already have
// (its base, its main branch's head), for an incremental bundle.
type Plan struct {
	Project string   `json:"project"`
	Home    string   `json:"home"`
	Commits []string `json:"commits"`
}

// Probe is what a target reports about a project: whether the repository
// exists there and which of the asked commits it has.
type Probe struct {
	Exists bool            `json:"exists"`
	Has    map[string]bool `json:"has"`
}

func newID() string {
	var b [6]byte
	rand.Read(b[:])
	return "ho-" + hex.EncodeToString(b[:])
}

func (p Paths) git(ctx context.Context) git { return git{ctx: ctx, env: p.Env} }

// PlanFor reports the commits a target may have of a's project.
func PlanFor(ctx context.Context, a wire.Agent, p Paths) Plan {
	g := p.git(ctx)
	plan := Plan{Project: a.Project, Home: p.Home}
	if r := g.repoInfo(a.Dir()); r != nil && r.base != "" {
		plan.Project = r.path
		plan.Commits = append(plan.Commits, r.base)
		if r.mainBranch != "" {
			if head := g.try(r.path, "rev-parse", "--verify", "-q", "refs/heads/"+r.mainBranch); head != "" && head != r.base {
				plan.Commits = append(plan.Commits, head)
			}
		}
	}
	return plan
}

// ProbeRepo answers a Plan on the target: path is the source's, mapped
// from its home to this one.
func ProbeRepo(ctx context.Context, path, home string, commits []string, p Paths) Probe {
	g := p.git(ctx)
	path = MapPath(path, home, p.Home)
	probe := Probe{Has: map[string]bool{}}
	if !g.isRepoAt(path) {
		return probe
	}
	probe.Exists = true
	for _, c := range commits {
		if shaRE.MatchString(c) {
			probe.Has[c] = g.ok(path, "cat-file", "-e", c+"^{commit}")
		}
	}
	return probe
}

// MapPath is path on this machine: under the source's home it moves to
// this home.
func MapPath(path, sourceHome, home string) string {
	if path == "" || sourceHome == "" || home == "" {
		return path
	}
	sourceHome = strings.TrimRight(sourceHome, "/")
	here := strings.TrimRight(home, "/")
	if sourceHome != here && (path == sourceHome || strings.HasPrefix(path, sourceHome+"/")) {
		return here + path[len(sourceHome):]
	}
	return path
}

// Pack writes a's bundle into dir (created, 0700): its conversation and
// its code, the latter incremental from have (commits the target has).
// machine is this machine's short name.
func Pack(ctx context.Context, a wire.Agent, machine string, have []string, dir string, p Paths) (*Manifest, error) {
	if a.Kind == wire.KindShell {
		return nil, Errorf("invalid", "a shell has no conversation to move")
	}
	if !localRE.MatchString(localPart(a.ID)) {
		return nil, Errorf("invalid", "bad agent id %q", a.ID)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	g := p.git(ctx)
	m := &Manifest{Version: Version, ID: newID(), Created: time.Now().Unix()}
	m.Source.Machine, m.Source.Home = machine, p.Home
	m.Agent = AgentInfo{LocalID: localPart(a.ID), Kind: a.Kind, Profile: a.Profile, Name: a.Name, Task: a.Task, Created: a.Created,
		Parent: a.Parent, Depth: a.Depth, LetParentAnswer: a.LetParentAnswer}
	m.Project = ProjectInfo{Path: a.Project, Worktree: a.Worktree, Branch: a.Branch}
	if r := g.repoInfo(a.Dir()); r != nil && r.base != "" {
		m.Project = ProjectInfo{Path: r.path, Worktree: r.worktree, Branch: r.branch, MainBranch: r.mainBranch, Base: r.base, Remote: r.remote}
		handoff, err := g.handoffCommit(r.top, r.base, m.ID)
		if err != nil {
			return nil, err
		}
		m.Project.Handoff = handoff
		kind, newest, err := g.writeBundle(r, handoff, m.ID, have, filepath.Join(dir, BundleFile))
		if err != nil {
			return nil, err
		}
		m.Project.Bundle, m.Project.Have = kind, newest
	}
	if sessionRE.MatchString(a.SessionID) {
		if path, name := findTranscript(a, p); path != "" {
			if err := copyFile(path, filepath.Join(dir, TranscriptFile), 0o600); err != nil {
				return nil, err
			}
			m.Agent.SessionID, m.Agent.Transcript = a.SessionID, name
		}
	}
	data, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), append(data, '\n'), 0o600); err != nil {
		return nil, err
	}
	return m, nil
}

func localPart(id string) string {
	if _, l, ok := strings.Cut(id, "/"); ok {
		return l
	}
	return id
}

// ClaudeSlug is the folder name Claude Code keeps a directory's sessions
// in.
func ClaudeSlug(dir string) string {
	return regexp.MustCompile(`[^A-Za-z0-9-]`).ReplaceAllString(dir, "-")
}

// findTranscript is the agent's conversation file and its name in the
// bundle ("" when there is none).
func findTranscript(a wire.Agent, p Paths) (string, string) {
	switch a.Kind {
	case wire.KindClaude:
		name := a.SessionID + ".jsonl"
		candidate := filepath.Join(p.ClaudeHome, "projects", ClaudeSlug(a.Dir()), name)
		if isFile(candidate) {
			return candidate, name
		}
		// A session started elsewhere (a resumed one keeps its folder).
		matches, _ := filepath.Glob(filepath.Join(p.ClaudeHome, "projects", "*", name))
		if len(matches) > 0 {
			return matches[0], name
		}
	case wire.KindCodex:
		if path := CodexRollout(p.CodexHome, a.SessionID); path != "" {
			sessions := filepath.Join(p.CodexHome, "sessions")
			if rel, err := filepath.Rel(sessions, path); err == nil && !strings.HasPrefix(rel, "..") {
				return path, rel
			}
		}
	}
	return "", ""
}

// CodexRollout is the newest rollout file of a Codex session.
func CodexRollout(codexHome, session string) string {
	days, _ := filepath.Glob(filepath.Join(codexHome, "sessions", "[0-9]*", "[0-9]*", "[0-9]*"))
	sort.Sort(sort.Reverse(sort.StringSlice(days)))
	for _, day := range days {
		matches, _ := filepath.Glob(filepath.Join(day, "rollout-*"+session+".jsonl"))
		if len(matches) > 0 {
			sort.Strings(matches)
			return matches[len(matches)-1]
		}
	}
	return ""
}

func isFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}

// Placed is what Unpack made: where the agent runs on this machine and
// whether its conversation is there to resume.
type Placed struct {
	Project, Worktree, Branch string
	Resume                    bool
}

// LoadManifest reads and checks a bundle's manifest.
func LoadManifest(dir string) (*Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		return nil, Errorf("manifest", "cannot read manifest.json: %v", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, Errorf("manifest", "cannot read manifest.json: %v", err)
	}
	if m.Version != Version || !idRE.MatchString(m.ID) {
		return nil, Errorf("manifest", "unsupported manifest version or id")
	}
	if m.Agent.Kind != wire.KindClaude && m.Agent.Kind != wire.KindCodex {
		return nil, Errorf("manifest", "unknown agent kind")
	}
	if !localRE.MatchString(m.Agent.LocalID) {
		return nil, Errorf("manifest", "bad agent id")
	}
	if m.Agent.SessionID != "" && !sessionRE.MatchString(m.Agent.SessionID) {
		return nil, Errorf("manifest", "bad session id")
	}
	for _, sha := range []string{m.Project.Base, m.Project.Handoff, m.Project.Have} {
		if sha != "" && !shaRE.MatchString(sha) {
			return nil, Errorf("manifest", "bad commit in the manifest")
		}
	}
	for _, path := range []string{m.Project.Path, m.Project.Worktree, m.Source.Home} {
		if path != "" && (!filepath.IsAbs(path) || filepath.Clean(path) != path) {
			return nil, Errorf("manifest", "paths must be clean and absolute")
		}
	}
	if m.Project.Path == "" {
		return nil, Errorf("manifest", "no project")
	}
	switch m.Project.Bundle {
	case "", "full", "incremental":
	default:
		return nil, Errorf("manifest", "unknown bundle kind")
	}
	if m.Project.Bundle != "" && (m.Project.Base == "" || m.Project.Handoff == "") {
		return nil, Errorf("manifest", "a bundle needs its base and handoff commits")
	}
	return &m, nil
}

// Unpack makes a bundle's agent ready to run here: its code checked out
// with the uncommitted work restored, its conversation placed.
func Unpack(ctx context.Context, dir string, p Paths) (*Manifest, Placed, error) {
	m, err := LoadManifest(dir)
	if err != nil {
		return nil, Placed{}, err
	}
	g := p.git(ctx)
	path := MapPath(m.Project.Path, m.Source.Home, p.Home)
	worktree := MapPath(m.Project.Worktree, m.Source.Home, p.Home)
	workdir := path
	if worktree != "" {
		workdir = worktree
	}
	if m.Project.Bundle != "" {
		fresh, err := g.fetchCode(m, filepath.Join(dir, BundleFile), path)
		if err != nil {
			return m, Placed{}, err
		}
		if err := g.checkOut(path, workdir, m.Project.Branch, m.Project.Base, worktree, m.Project.MainBranch, fresh); err != nil {
			return m, Placed{}, err
		}
		if err := g.restoreChanges(workdir, m.Project.Base, m.Project.Handoff); err != nil {
			return m, Placed{}, err
		}
	} else if st, err := os.Stat(workdir); err != nil || !st.IsDir() {
		return m, Placed{}, Errorf("missing_project", "no project at %s", workdir)
	}
	placed := Placed{Project: path, Worktree: worktree, Branch: m.Project.Branch}
	if m.Agent.SessionID != "" && m.Agent.Transcript != "" && isFile(filepath.Join(dir, TranscriptFile)) {
		var mapping *[2]string
		if src := strings.TrimRight(m.Source.Home, "/"); src != "" && src != strings.TrimRight(p.Home, "/") {
			mapping = &[2]string{src, strings.TrimRight(p.Home, "/")}
		}
		if err := placeTranscript(filepath.Join(dir, TranscriptFile), m.Agent, mapping, workdir, p); err != nil {
			return m, Placed{}, err
		}
		placed.Resume = true
	}
	return m, placed, nil
}

// placeTranscript puts a travelled conversation where the agent CLI looks
// for it, every "cwd" under the source's home moved to this home.
func placeTranscript(source string, a AgentInfo, mapping *[2]string, workdir string, p Paths) error {
	var target string
	switch a.Kind {
	case wire.KindClaude:
		cwd := firstCwd(source)
		if cwd != "" && mapping != nil {
			cwd = MapPath(cwd, mapping[0], mapping[1])
		}
		if cwd == "" {
			cwd = workdir
		}
		target = filepath.Join(p.ClaudeHome, "projects", ClaudeSlug(cwd), a.SessionID+".jsonl")
	case wire.KindCodex:
		rel := filepath.Clean(a.Transcript)
		if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") || !strings.HasSuffix(rel, ".jsonl") {
			return Errorf("manifest", "bad transcript name %q", a.Transcript)
		}
		target = filepath.Join(p.CodexHome, "sessions", rel)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	temp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	w := bufio.NewWriter(temp)
	r := bufio.NewReader(in)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			if mapping != nil && bytes.Contains(line, []byte(`"cwd"`)) {
				line = rewriteLine(line, *mapping)
			}
			if _, werr := w.Write(line); werr != nil {
				temp.Close()
				return werr
			}
		}
		if err != nil {
			break
		}
	}
	if err := w.Flush(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), target)
}

// rewriteLine moves every "cwd" value of one JSON line from the source's
// home to this one; a line that does not parse stays as it is.
func rewriteLine(line []byte, mapping [2]string) []byte {
	trimmed := bytes.TrimRight(line, "\r\n")
	d := json.NewDecoder(bytes.NewReader(trimmed))
	d.UseNumber()
	var v any
	if d.Decode(&v) != nil {
		return line
	}
	v = rewriteCwd(v, mapping)
	var out bytes.Buffer
	e := json.NewEncoder(&out)
	e.SetEscapeHTML(false)
	if e.Encode(v) != nil {
		return line
	}
	return out.Bytes() // Encode ends with a newline
}

func rewriteCwd(v any, mapping [2]string) any {
	switch x := v.(type) {
	case map[string]any:
		for k, item := range x {
			if s, ok := item.(string); ok && k == "cwd" {
				x[k] = MapPath(s, mapping[0], mapping[1])
			} else {
				x[k] = rewriteCwd(item, mapping)
			}
		}
	case []any:
		for i := range x {
			x[i] = rewriteCwd(x[i], mapping)
		}
	}
	return v
}

var cwdRE = regexp.MustCompile(`"cwd"\s*:\s*"((?:[^"\\]|\\.)*)"`)

func firstCwd(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 64<<10), 64<<20)
	for s.Scan() {
		if m := cwdRE.FindSubmatch(s.Bytes()); m != nil {
			var cwd string
			if json.Unmarshal(append(append([]byte{'"'}, m[1]...), '"'), &cwd) == nil {
				return cwd
			}
		}
	}
	return ""
}
