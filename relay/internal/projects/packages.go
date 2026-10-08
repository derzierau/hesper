package projects

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Monorepo packages, from the manifests at a repository's root:
//
//   - pnpm: pnpm-workspace.yaml "packages" globs (folders with package.json)
//   - npm / yarn: package.json "workspaces" (a list, or {packages: […]})
//   - Turborepo: turbo.json next to npm/pnpm workspaces (tool "turbo")
//   - Lerna: lerna.json "packages" (default packages/*)
//   - Nx: nx.json; every project.json below the root (depth ≤ 4)
//   - Go: go.work "use" directives (folders with go.mod)
//   - Cargo: Cargo.toml [workspace] members (minus exclude; folders with
//     Cargo.toml)
//
// Globs: * ? [..] within a segment, ** across folders (≤ 6 deep), "!" to
// exclude. node_modules, .git and dot folders are never entered. At most
// maxPackages per repository. Cached per root by the manifests' size and
// modification time: a look costs seven stats.

const (
	maxPackages = 2000
	maxGlobDeep = 6
	nxDepth     = 4
)

var markers = []string{"package.json", "pnpm-workspace.yaml", "lerna.json", "nx.json", "turbo.json", "go.work", "Cargo.toml"}

type pkgCache struct {
	stamp string
	pkgs  []wire.DetectedPackage
}

// packagesOf is a checkout's packages (cached by its manifests).
func (d *detector) packagesOf(root string) []wire.DetectedPackage {
	stamp := markerStamp(root)
	d.mu.Lock()
	c, ok := d.pkgs[root]
	d.mu.Unlock()
	if ok && c.stamp == stamp {
		return c.pkgs
	}
	var pkgs []wire.DetectedPackage
	if stamp != "" {
		pkgs = DetectPackages(root)
	}
	d.mu.Lock()
	d.pkgs[root] = pkgCache{stamp: stamp, pkgs: pkgs}
	d.mu.Unlock()
	return pkgs
}

// cachedPackages is what packagesOf found last (no file system access).
func (d *detector) cachedPackages(root string) []wire.DetectedPackage {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.pkgs[root].pkgs
}

func markerStamp(root string) string {
	var b strings.Builder
	for _, m := range markers {
		if st, err := os.Stat(filepath.Join(root, m)); err == nil {
			fmt.Fprintf(&b, "%s:%d:%d;", m, st.Size(), st.ModTime().UnixNano())
		}
	}
	return b.String()
}

// DetectPackages finds the monorepo packages of the checkout at root,
// sorted by path.
func DetectPackages(root string) []wire.DetectedPackage {
	found := map[string]wire.DetectedPackage{}
	add := func(rel, name, tool string) {
		if rel == "" || rel == "." || len(found) >= maxPackages {
			return
		}
		if _, ok := found[rel]; ok {
			return
		}
		if name == "" {
			name = filepath.Base(rel)
		}
		found[rel] = wire.DetectedPackage{Path: rel, Name: name, Tool: tool}
	}
	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(root, name))
		return err == nil
	}
	turbo := exists("turbo.json")
	jsTool := func(tool string) string {
		if turbo {
			return "turbo"
		}
		return tool
	}
	jsAdd := func(globs []string, tool string) {
		for _, rel := range expandGlobs(root, globs, "package.json") {
			add(rel, jsonName(filepath.Join(root, rel, "package.json")), tool)
		}
	}
	// pnpm
	if globs := pnpmGlobs(filepath.Join(root, "pnpm-workspace.yaml")); len(globs) > 0 {
		jsAdd(globs, jsTool("pnpm"))
	}
	// npm / yarn
	var pj struct {
		Workspaces json.RawMessage `json:"workspaces"`
	}
	if readJSON(filepath.Join(root, "package.json"), &pj) == nil && len(pj.Workspaces) > 0 {
		var list []string
		if json.Unmarshal(pj.Workspaces, &list) != nil {
			var obj struct {
				Packages []string `json:"packages"`
			}
			json.Unmarshal(pj.Workspaces, &obj)
			list = obj.Packages
		}
		jsAdd(list, jsTool("npm"))
	}
	// Lerna
	var lerna struct {
		Packages []string `json:"packages"`
	}
	if readJSON(filepath.Join(root, "lerna.json"), &lerna) == nil {
		globs := lerna.Packages
		if len(globs) == 0 {
			globs = []string{"packages/*"}
		}
		jsAdd(globs, "lerna")
	}
	// Nx
	if exists("nx.json") {
		walk(root, "", nxDepth, func(rel string) {
			if rel == "" {
				return
			}
			if _, err := os.Stat(filepath.Join(root, rel, "project.json")); err == nil {
				name := jsonName(filepath.Join(root, rel, "project.json"))
				if name == "" {
					name = jsonName(filepath.Join(root, rel, "package.json"))
				}
				add(rel, name, "nx")
			}
		})
	}
	// go.work
	for _, use := range goWorkUses(filepath.Join(root, "go.work")) {
		rel := cleanRel(use)
		if rel == "" {
			continue
		}
		if mod := goModule(filepath.Join(root, rel, "go.mod")); mod != "" {
			add(rel, filepath.Base(mod), "go")
		}
	}
	// Cargo
	if members, exclude := cargoMembers(filepath.Join(root, "Cargo.toml")); len(members) > 0 {
		for _, e := range exclude {
			members = append(members, "!"+e)
		}
		for _, rel := range expandGlobs(root, members, "Cargo.toml") {
			add(rel, cargoName(filepath.Join(root, rel, "Cargo.toml")), "cargo")
		}
	}
	out := make([]wire.DetectedPackage, 0, len(found))
	for _, p := range found {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// packageAt is the deepest package containing rel (a path relative to the
// checkout), nil when none does.
func packageAt(pkgs []wire.DetectedPackage, rel string) *wire.DetectedPackage {
	var best *wire.DetectedPackage
	for i := range pkgs {
		p := &pkgs[i]
		if rel == p.Path || strings.HasPrefix(rel, p.Path+"/") {
			if best == nil || len(p.Path) > len(best.Path) {
				best = p
			}
		}
	}
	return best
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func jsonName(path string) string {
	var v struct {
		Name string `json:"name"`
	}
	readJSON(path, &v)
	return v.Name
}

func cleanRel(p string) string {
	p = strings.TrimSpace(p)
	p = strings.Trim(p, `"'`)
	p = filepath.ToSlash(filepath.Clean(p))
	p = strings.TrimPrefix(p, "./")
	if p == "." || strings.HasPrefix(p, "../") || p == ".." || filepath.IsAbs(p) {
		return ""
	}
	return p
}

// skipDir: folders never entered by globs or the Nx walk.
func skipDir(name string) bool {
	return name == "node_modules" || strings.HasPrefix(name, ".")
}

// skipWalk: also build output, which the Nx walk does not enter.
func skipWalk(name string) bool {
	return skipDir(name) || name == "dist" || name == "target" || name == "build"
}

// walk calls fn for every folder below root (rel paths), depth-limited.
func walk(root, rel string, depth int, fn func(rel string)) {
	fn(rel)
	if depth == 0 {
		return
	}
	entries, err := os.ReadDir(filepath.Join(root, rel))
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() && !skipWalk(e.Name()) {
			walk(root, joinRel(rel, e.Name()), depth-1, fn)
		}
	}
}

func joinRel(a, b string) string {
	if a == "" {
		return b
	}
	return a + "/" + b
}

// expandGlobs is the folders (relative to root) the globs match that hold
// manifest; "!" globs remove matches.
func expandGlobs(root string, globs []string, manifest string) []string {
	include := map[string]bool{}
	var exclude []string
	for _, g := range globs {
		g = strings.TrimSpace(g)
		if strings.HasPrefix(g, "!") {
			exclude = append(exclude, strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(g, "!"), "./"), "/"))
			continue
		}
		g = strings.TrimSuffix(strings.TrimPrefix(g, "./"), "/")
		if g == "" || strings.HasPrefix(g, "/") || strings.Contains(g, "..") {
			continue
		}
		matchSegs(root, "", strings.Split(g, "/"), 0, func(rel string) {
			if len(include) < maxPackages {
				include[rel] = true
			}
		})
	}
	var out []string
	for rel := range include {
		if _, err := os.Stat(filepath.Join(root, rel, manifest)); err != nil {
			continue
		}
		excluded := false
		for _, e := range exclude {
			if globMatch(e, rel) {
				excluded = true
				break
			}
		}
		if !excluded {
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out
}

func matchSegs(root, rel string, segs []string, deep int, fn func(string)) {
	if len(segs) == 0 {
		if rel != "" {
			fn(rel)
		}
		return
	}
	seg := segs[0]
	if seg == "**" {
		matchSegs(root, rel, segs[1:], deep, fn)
		if deep >= maxGlobDeep {
			return
		}
		for _, name := range subdirs(filepath.Join(root, rel)) {
			matchSegs(root, joinRel(rel, name), segs, deep+1, fn)
		}
		return
	}
	if !strings.ContainsAny(seg, "*?[") {
		if st, err := os.Stat(filepath.Join(root, rel, seg)); err == nil && st.IsDir() {
			matchSegs(root, joinRel(rel, seg), segs[1:], deep, fn)
		}
		return
	}
	for _, name := range subdirs(filepath.Join(root, rel)) {
		if ok, _ := filepath.Match(seg, name); ok {
			matchSegs(root, joinRel(rel, name), segs[1:], deep, fn)
		}
	}
}

func subdirs(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && !skipDir(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out
}

// globMatch matches a slash path against a glob with ** (exclusions).
func globMatch(glob, p string) bool {
	gs, ps := strings.Split(glob, "/"), strings.Split(p, "/")
	var match func(i, j int) bool
	match = func(i, j int) bool {
		if i == len(gs) {
			return j == len(ps)
		}
		if gs[i] == "**" {
			for k := j; k <= len(ps); k++ {
				if match(i+1, k) {
					return true
				}
			}
			return false
		}
		if j == len(ps) {
			return false
		}
		if ok, _ := filepath.Match(gs[i], ps[j]); !ok {
			return false
		}
		return match(i+1, j+1)
	}
	return match(0, 0)
}

// pnpmGlobs reads pnpm-workspace.yaml's packages list (block or flow
// style; the only key hesperd needs, read without a YAML library).
func pnpmGlobs(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	in := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.Index(line, " #"); i >= 0 {
			line = line[:i]
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indented := line[0] == ' ' || line[0] == '\t' || line[0] == '-'
		if !indented {
			in = false
			if rest, ok := strings.CutPrefix(trimmed, "packages:"); ok {
				rest = strings.TrimSpace(rest)
				if strings.HasPrefix(rest, "[") {
					out = append(out, flowList(rest)...)
				} else {
					in = true
				}
			}
			continue
		}
		if in && strings.HasPrefix(trimmed, "-") {
			out = append(out, unquote(strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))))
		}
	}
	return out
}

func flowList(s string) []string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	if i := strings.IndexByte(s, ']'); i >= 0 {
		s = s[:i]
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		if v := unquote(strings.TrimSpace(part)); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}

// goWorkUses reads go.work's use directives.
func goWorkUses(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	block := false
	for _, line := range strings.Split(string(data), "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		switch {
		case block && line == ")":
			block = false
		case block && line != "":
			out = append(out, unquote(line))
		case line == "use (" || line == "use(":
			block = true
		case strings.HasPrefix(line, "use "):
			out = append(out, unquote(strings.TrimSpace(strings.TrimPrefix(line, "use "))))
		}
	}
	return out
}

func goModule(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "module "); ok {
			return unquote(strings.TrimSpace(rest))
		}
	}
	return ""
}

// tomlTable reads one table's top-level keys from a TOML file: string
// values and string arrays (also over several lines), enough for Cargo's
// [workspace] and [package].
func tomlTable(path, table string) map[string][]string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	out := map[string][]string{}
	in := false
	var key string
	var acc strings.Builder
	collecting := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(stripComment(line))
		if collecting {
			acc.WriteString(line)
			if strings.Contains(line, "]") {
				out[key] = flowList(acc.String())
				collecting = false
			}
			continue
		}
		if strings.HasPrefix(line, "[") {
			in = line == "["+table+"]"
			continue
		}
		if !in || line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if strings.HasPrefix(v, "[") {
			if strings.Contains(v, "]") {
				out[key] = flowList(v)
			} else {
				acc.Reset()
				acc.WriteString(v)
				collecting = true
			}
			continue
		}
		out[key] = []string{unquote(v)}
	}
	return out
}

// stripComment cuts a # comment that is not inside quotes.
func stripComment(line string) string {
	var quote byte
	for i := 0; i < len(line); i++ {
		switch c := line[i]; {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '#':
			return line[:i]
		}
	}
	return line
}

func cargoMembers(path string) (members, exclude []string) {
	t := tomlTable(path, "workspace")
	return t["members"], t["exclude"]
}

func cargoName(path string) string {
	if v := tomlTable(path, "package")["name"]; len(v) == 1 {
		return v[0]
	}
	return ""
}
