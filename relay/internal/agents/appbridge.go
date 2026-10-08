package agents

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// App control (docs/rebuild-contract.md "As built — app control"):
// Hesper.app registers its control connection with app.register; the
// app.* methods any other local client calls (hesperctl open, wall, desk)
// are sent to the app as JSON-RPC requests on that connection (ids
// "app-N", strings, so they never collide with a client's own numeric
// ids) and its response is relayed back. One app at a time: the latest
// registration wins; it ends with its connection. Local only: the host
// service (internal/host) has no app.* methods.

// The methods forwarded to the registered app: app.state, app.open,
// app.wall.set, app.desk (dispatch in server.go).

// appTimeout bounds one forwarded call.
const appTimeout = 10 * time.Second

// codeTimeout is data.code of a call the app did not answer in time
// (hesperctl exits 6).
const codeTimeout = "timeout"

type appBridge struct {
	mu      sync.Mutex
	app     *ctrl
	next    int64
	pending map[string]appCall
	timeout time.Duration
}

// appCall is a request sent to an app, waiting for its response.
type appCall struct {
	app *ctrl
	ch  chan *wire.Response
}

func newAppBridge() *appBridge {
	return &appBridge{pending: map[string]appCall{}, timeout: appTimeout}
}

// intercept handles what the read loop of a control connection must see
// itself: app.register (it needs the connection) and responses to the
// requests sent to the app (lines without a method). False: dispatch as
// usual.
func (b *appBridge) intercept(c *ctrl, req *wire.Request, line []byte) bool {
	switch {
	case req.Method == "app.register":
		b.register(c)
		if len(req.ID) > 0 {
			c.reply(req.ID, struct{}{}, nil)
		}
		return true
	case req.Method == "" && len(req.ID) > 0:
		var res wire.Response
		if json.Unmarshal(line, &res) == nil {
			b.deliver(c, &res)
		}
		return true // a response is never answered
	}
	return false
}

func (b *appBridge) register(c *ctrl) {
	b.mu.Lock()
	old := b.app
	b.app = c
	b.mu.Unlock()
	if old == c {
		return
	}
	if old != nil {
		b.fail(old, "another Hesper.app registered")
	}
	go func() {
		<-c.done
		b.drop(c)
	}()
}

// drop forgets c (its connection closed) and fails its calls.
func (b *appBridge) drop(c *ctrl) {
	b.mu.Lock()
	if b.app == c {
		b.app = nil
	}
	b.mu.Unlock()
	b.fail(c, "Hesper.app disconnected")
}

// fail ends every call waiting on c with unavailable.
func (b *appBridge) fail(c *ctrl, why string) {
	b.mu.Lock()
	var chans []chan *wire.Response
	for id, call := range b.pending {
		if call.app == c {
			chans = append(chans, call.ch)
			delete(b.pending, id)
		}
	}
	b.mu.Unlock()
	for _, ch := range chans {
		ch <- &wire.Response{Error: &wire.RPCError{Code: wire.RPCServer, Message: why, Data: &wire.ErrorData{Code: wire.CodeUnavailable}}}
	}
}

func (b *appBridge) deliver(c *ctrl, res *wire.Response) {
	var id string
	if json.Unmarshal(res.ID, &id) != nil {
		return
	}
	b.mu.Lock()
	call, ok := b.pending[id]
	ok = ok && call.app == c // only the connection it went to answers it
	if ok {
		delete(b.pending, id)
	}
	b.mu.Unlock()
	if ok {
		call.ch <- res
	}
}

// forward sends method to the app and waits for its answer.
func (b *appBridge) forward(method string, params json.RawMessage) (any, error) {
	b.mu.Lock()
	c := b.app
	if c == nil {
		b.mu.Unlock()
		return nil, wire.Errorf(wire.CodeUnavailable, "Hesper.app is not running (or not connected to hesperd)")
	}
	b.next++
	id := "app-" + strconv.FormatInt(b.next, 10)
	ch := make(chan *wire.Response, 1)
	b.pending[id] = appCall{app: c, ch: ch}
	timeout := b.timeout
	b.mu.Unlock()

	rawID, _ := json.Marshal(id)
	if len(params) == 0 {
		params = json.RawMessage("{}")
	}
	if err := c.write(wire.Request{JSONRPC: "2.0", ID: rawID, Method: method, Params: params}); err != nil {
		b.forget(id)
		return nil, wire.Errorf(wire.CodeUnavailable, "Hesper.app: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	select {
	case res := <-ch:
		if res.Error != nil {
			return nil, res.Error.Err()
		}
		if len(res.Result) == 0 {
			return struct{}{}, nil
		}
		return res.Result, nil
	case <-ctx.Done():
		b.forget(id)
		return nil, wire.Errorf(codeTimeout, "Hesper.app did not answer %s within %s", method, timeout)
	}
}

func (b *appBridge) forget(id string) {
	b.mu.Lock()
	delete(b.pending, id)
	b.mu.Unlock()
}
