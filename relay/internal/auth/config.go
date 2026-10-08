package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/derzierau/hesper/relay/pkg/client"
)

type Config struct {
	PublicURL        string   `json:"publicURL"`
	GitHubClientID   string   `json:"githubClientId"`
	GitHubSecretFile string   `json:"githubSecretFile"`
	AllowedGitHubIDs []string `json:"allowedGitHubIds"`
	AppCallbacks     []string `json:"appCallbacks,omitempty"`
}

func LoadConfig(path string) (Config, error) {
	var c Config
	data, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err = json.Unmarshal(data, &c); err != nil {
		return c, err
	}
	if _, err := client.Origin(c.PublicURL); err != nil {
		return c, err
	}
	if c.GitHubClientID == "" || c.GitHubSecretFile == "" {
		return c, fmt.Errorf("GitHub client ID, and secret file are required")
	}
	for _, id := range c.AllowedGitHubIDs {
		if !regexp.MustCompile(`^[1-9][0-9]*$`).MatchString(id) {
			return c, fmt.Errorf("allowlist entries must be numeric GitHub IDs")
		}
	}
	for _, callback := range c.AppCallbacks {
		u, err := url.Parse(callback)
		if err != nil || u.Scheme == "" || u.Scheme == "http" || u.Scheme == "javascript" || u.Scheme == "data" || u.User != nil || u.Fragment != "" {
			return c, fmt.Errorf("invalid application callback")
		}
	}
	c.PublicURL = strings.TrimRight(c.PublicURL, "/")
	return c, nil
}

type Allowlist struct {
	mu  sync.RWMutex
	ids map[string]bool
}

func NewAllowlist(ids []string) *Allowlist { p := &Allowlist{}; p.Replace(ids); return p }
func (p *Allowlist) Replace(ids []string) {
	next := map[string]bool{}
	for _, id := range ids {
		next["github:"+id] = true
	}
	p.mu.Lock()
	p.ids = next
	p.mu.Unlock()
}
func (p *Allowlist) Allowed(id string) bool { p.mu.RLock(); defer p.mu.RUnlock(); return p.ids[id] }

// ReadSecret rejects files readable by group or other users. Secrets never enter logs.
func ReadSecret(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 4096 {
		return nil, fmt.Errorf("GitHub secret must be a regular file, at most 4096 bytes, mode 0600 or 0400")
	}
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(data)) == "" {
		return nil, fmt.Errorf("GitHub secret file is empty")
	}
	return data, nil
}
