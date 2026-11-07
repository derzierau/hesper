package host

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/derzierau/hesper/relay/pkg/client"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

// bigHistory answers sessions.pull with more than one relay message holds
// and everything else with a little.
type bigHistory struct{}

func (bigHistory) HostCall(_ context.Context, method string, _ json.RawMessage) (any, error) {
	if method == "sessions.pull" {
		return map[string]string{"data": strings.Repeat("x", protocol.MaxMessageBytes)}, nil
	}
	return map[string]string{"plan": "small"}, nil
}
func (bigHistory) HostHint() json.RawMessage { return json.RawMessage(`{}`) }
func (bigHistory) Changes() <-chan struct{}  { return nil }
func (bigHistory) Exportable(string) error   { return nil }

// An answer the relay would not take is never sent: the relay drops the
// whole connection for one message over its limit, and every controller
// of the host with it. The host answers too_large instead and the link
// keeps working.
func TestAnswersOverTheRelaysLimitAreRefusedNotSent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conns := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{protocol.Subprotocol}})
		if err != nil {
			return
		}
		c.SetReadLimit(protocol.MaxMessageBytes) // as the relay (internal/transport)
		conns <- c
		<-r.Context().Done()
	}))
	defer server.Close()
	reg, _ := testRegistry(t)
	runner := &Runner{Service: &Service{Agents: reg, Sessions: bigHistory{}}, PollInterval: time.Hour,
		Credentials: protocol.Credentials{Relay: server.URL, DeviceID: "mini-1", Role: "host", Token: "t"}}
	link, err := client.Dial(ctx, runner.Credentials)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); runner.Serve(ctx, link) }()
	defer func() { cancel(); link.Close(); <-done }()
	relay := <-conns
	defer relay.CloseNow()

	call := func(id, method string) protocol.Message {
		t.Helper()
		m := protocol.Message{Version: protocol.Version, Type: "request", ID: id, ControllerID: "laptop-1", Method: method,
			Params: json.RawMessage(`{}`), Deadline: time.Now().Add(5 * time.Second).UnixMilli()}
		if err := wsjson.Write(ctx, relay, m); err != nil {
			t.Fatal(err)
		}
		for {
			var reply protocol.Message
			if err := wsjson.Read(ctx, relay, &reply); err != nil {
				t.Fatalf("%s: the link broke: %v", method, err)
			}
			if reply.Type == "result" && reply.ID == id {
				return reply
			}
		}
	}
	if reply := call("r-1", "sessions.pull"); reply.Error == nil || reply.Error.Code != "too_large" || len(reply.Result) != 0 {
		t.Fatalf("oversized answer: error %v, %d bytes of result", reply.Error, len(reply.Result))
	}
	if reply := call("r-2", "sessions.plan"); reply.Error != nil || !strings.Contains(string(reply.Result), "small") {
		t.Fatalf("next answer: error %v, result %s", reply.Error, reply.Result)
	}
}
