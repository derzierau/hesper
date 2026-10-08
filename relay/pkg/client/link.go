// Package client supplies enrollment and transport clients for host and controller applications.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

func Origin(value string) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil {
		return nil, err
	}
	local := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
	if u.Host == "" || (u.Scheme != "https" && !(local && u.Scheme == "http")) || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("relay must be an HTTPS origin (HTTP allowed only on loopback)")
	}
	u.Path = ""
	return u, nil
}
func Pair(ctx context.Context, origin, invitation, name string) (protocol.Credentials, error) {
	var credentials protocol.Credentials
	u, err := Origin(origin)
	if err != nil {
		return credentials, err
	}
	u.Path = "/v1/pair"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(protocol.JSON(protocol.PairRequest{Invitation: strings.TrimSpace(invitation), Name: name})))
	if err != nil {
		return credentials, err
	}
	req.Header.Set("Content-Type", "application/json")
	httpClient := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := httpClient.Do(req)
	if err != nil {
		return credentials, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return credentials, fmt.Errorf("pairing failed (HTTP %d); invitation may be expired or consumed", res.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&credentials); err != nil {
		return credentials, err
	}
	credentials.Relay = origin
	return credentials, nil
}
func LoadCredentials(path string) (protocol.Credentials, error) {
	var c protocol.Credentials
	data, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, err
	}
	if _, err := Origin(c.Relay); err != nil {
		return c, err
	}
	if !protocol.ValidID(c.Token) || !protocol.ValidID(c.DeviceID) || (c.Role != "host" && c.Role != "controller") {
		return c, fmt.Errorf("invalid credentials file")
	}
	return c, nil
}
func SaveCredentials(path string, c protocol.Credentials) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := json.NewEncoder(file).Encode(c); err != nil {
		os.Remove(path)
		return err
	}
	return file.Sync()
}

type Link struct{ conn *websocket.Conn }

func Dial(ctx context.Context, c protocol.Credentials) (*Link, error) {
	u, err := Origin(c.Relay)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = "/v1/connect"
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, res, err := websocket.Dial(dialCtx, u.String(), &websocket.DialOptions{
		Subprotocols: []string{protocol.Subprotocol}, HTTPHeader: http.Header{"Authorization": []string{"Bearer " + c.Token}},
		HTTPClient: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	})
	if err != nil {
		if res != nil && (res.StatusCode == 401 || res.StatusCode == 403) {
			return nil, protocol.Err("unauthorized", "Credential rejected; pair this device again")
		}
		return nil, err
	}
	conn.SetReadLimit(protocol.MaxMessageBytes)
	if conn.Subprotocol() != protocol.Subprotocol {
		conn.CloseNow()
		return nil, fmt.Errorf("relay does not support ghosty.v2")
	}
	return &Link{conn}, nil
}
func (l *Link) Read(ctx context.Context) (protocol.Message, error) {
	var m protocol.Message
	if err := wsjson.Read(ctx, l.conn, &m); err != nil {
		return m, err
	}
	if m.Version != protocol.Version {
		return m, protocol.Err("protocol_version", "Unsupported version")
	}
	return m, nil
}
func (l *Link) Send(ctx context.Context, m protocol.Message) error {
	m.Version = protocol.Version
	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return wsjson.Write(writeCtx, l.conn, m)
}
func (l *Link) Ping(ctx context.Context) error {
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return l.conn.Ping(pingCtx)
}
func (l *Link) Close() { l.conn.CloseNow() }
