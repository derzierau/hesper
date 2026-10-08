package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/derzierau/hesper/relay/pkg/protocol"
)

type GitHub struct {
	ClientID, Secret, Callback string
	Client                     *http.Client
}

func (g *GitHub) AuthorizationURL(state, verifier string) string {
	q := url.Values{"client_id": {g.ClientID}, "redirect_uri": {g.Callback}, "state": {state}, "code_challenge": {Challenge(verifier)}, "code_challenge_method": {"S256"}, "scope": {""}, "allow_signup": {"false"}}
	return "https://github.com/login/oauth/authorize?" + q.Encode()
}
func (g *GitHub) Identify(ctx context.Context, code, verifier string) (Principal, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	client := g.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	form := url.Values{"client_id": {g.ClientID}, "client_secret": {g.Secret}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {g.Callback}}
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://github.com/login/oauth/access_token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return Principal{}, protocol.Err("identity_unavailable", "GitHub sign-in is temporarily unavailable")
	}
	var token struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	err = json.NewDecoder(io.LimitReader(res.Body, 16*1024)).Decode(&token)
	res.Body.Close()
	if err != nil || res.StatusCode != 200 || token.AccessToken == "" || token.Error != "" {
		return Principal{}, protocol.Err("unauthorized", "GitHub authorization could not be verified")
	}
	req, _ = http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user", nil)
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "hesper-relay")
	res, err = client.Do(req)
	if err != nil {
		return Principal{}, protocol.Err("identity_unavailable", "GitHub identity is temporarily unavailable")
	}
	defer res.Body.Close()
	var user struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 64*1024)).Decode(&user); err != nil || res.StatusCode != 200 || user.ID <= 0 || user.Login == "" {
		return Principal{}, protocol.Err("unauthorized", "GitHub identity could not be verified")
	}
	// GitHub tokens are used only for this identity lookup, never persisted or
	// sent to clients. The relay's session service issues independent credentials.
	return Principal{ID: "github:" + strconv.FormatInt(user.ID, 10), Login: user.Login}, nil
}
func (g *GitHub) String() string { return fmt.Sprintf("GitHub identity provider (%s)", g.ClientID) }
