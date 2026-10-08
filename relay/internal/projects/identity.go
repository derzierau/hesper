package projects

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"hash/fnv"
	"net"
	"path"
	"path/filepath"
	"strings"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// NormalizeRemote turns a git remote URL into the identity of its
// repository: host (lower case, no user, no port) and path (no leading or
// trailing slashes, no ".git"), so ssh and https spellings of one
// repository are equal:
//
//	git@GitHub.com:Owner/Repo.git          → github.com/owner/repo
//	https://user@github.com/owner/repo/    → github.com/owner/repo
//	ssh://git@github.com:22/owner/repo.git → github.com/owner/repo
//
// Paths on github.com, gitlab.com and bitbucket.org are lower-cased too
// (those hosts ignore case); elsewhere the path keeps its case. Local
// remotes (a path, file://) become "file:<clean path>". "" for "".
func NormalizeRemote(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	var host, p string
	if i := strings.Index(s, "://"); i > 0 {
		scheme := strings.ToLower(s[:i])
		rest := s[i+3:]
		if scheme == "file" {
			return "file:" + path.Clean("/"+strings.TrimPrefix(rest, "localhost"))
		}
		host, p = rest, ""
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			host, p = rest[:j], rest[j+1:]
		}
	} else if colon := strings.IndexByte(s, ':'); colon > 0 && !strings.HasPrefix(s, "/") && (strings.IndexByte(s, '/') < 0 || colon < strings.IndexByte(s, '/')) {
		// scp-like: [user@]host:path
		host, p = s[:colon], s[colon+1:]
	} else {
		return "file:" + filepath.Clean(s)
	}
	if at := strings.LastIndexByte(host, '@'); at >= 0 {
		host = host[at+1:]
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(strings.ToLower(host), "[]")
	p = strings.TrimPrefix(p, "~/")
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	p = strings.Trim(p, "/")
	p = strings.TrimSuffix(p, ".git")
	p = strings.Trim(p, "/")
	switch host {
	case "github.com", "www.github.com", "ssh.github.com":
		host, p = "github.com", strings.ToLower(p)
	case "gitlab.com", "bitbucket.org":
		p = strings.ToLower(p)
	}
	if p == "" {
		return host
	}
	return host + "/" + p
}

// identityKey is the string a project's id is hashed from.
func identityKey(id wire.ProjectIdentity) string {
	var key string
	switch {
	case id.Remote != "":
		key = "git:" + id.Remote
	case id.Local != "":
		key = "local:" + id.Local
	default:
		return ""
	}
	if id.Package != "" {
		key += "#" + id.Package
	}
	return key
}

// ProjectID is the id of the project with this identity: "p-" and 16 hex
// characters of the SHA-256 of its identity. Every machine computes the
// same id for the same repository, so a remote agent's projectId is the
// one a local agent in the same repository has.
func ProjectID(id wire.ProjectIdentity) string {
	key := identityKey(id)
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return "p-" + hex.EncodeToString(sum[:8])
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// newLocalIdentity is a generated identity for a folder (or a repository
// without a remote).
func newLocalIdentity() string { return "l-" + randomHex(8) }

// Palette is the Tokyo Night colors projects get when none is set.
var Palette = []string{"#7aa2f7", "#bb9af7", "#73daca", "#ff9e64", "#7dcfff", "#9d7cd8", "#2ac3de", "#e0af68", "#9ece6a", "#b4f9f8"}

// AutoColor is a project's color when none is set: stable for its id.
func AutoColor(id string) string {
	h := fnv.New32a()
	h.Write([]byte(id))
	return Palette[h.Sum32()%uint32(len(Palette))]
}

// repoName is a repository's default name: the last part of its remote
// URL (as configured: its case kept, no ".git"), else its folder's name.
func repoName(remote, dir string) string {
	remote = strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(remote), "/"), ".git")
	if i := strings.LastIndexAny(remote, "/:"); i >= 0 && i < len(remote)-1 {
		return remote[i+1:]
	}
	return filepath.Base(dir)
}

func validColor(c string) bool {
	if len(c) != 7 || c[0] != '#' {
		return false
	}
	_, err := hex.DecodeString(c[1:])
	return err == nil
}
