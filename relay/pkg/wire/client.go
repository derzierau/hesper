package wire

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// JSON-RPC 2.0 envelopes, one per line.

type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type Notification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// RPCError is a JSON-RPC error; Data carries the daemon's code.
type RPCError struct {
	Code    int        `json:"code"`
	Message string     `json:"message"`
	Data    *ErrorData `json:"data,omitempty"`
}

type ErrorData struct {
	Code string `json:"code"`
	// AgentID (shared history): the agent a "live" session runs in.
	AgentID string `json:"agentId,omitempty"`
	// Processes (move work): agents.move's error "processes".
	Processes []Process `json:"processes,omitempty"`
	// Path (bring the folder): with "exists", the folder the target has.
	Path string `json:"path,omitempty"`
}

// JSON-RPC error numbers.
const (
	RPCParse          = -32700
	RPCInvalidRequest = -32600
	RPCNoMethod       = -32601
	RPCInvalidParams  = -32602
	RPCServer         = -32000
	// RPCOffline: data.code "offline" (closing agents).
	RPCOffline = -32010
)

// Err turns an RPCError into an *Error.
func (e *RPCError) Err() *Error {
	code, agent := CodeInvalid, ""
	var procs []Process
	if e.Data != nil && e.Data.Code != "" {
		code, agent, procs = e.Data.Code, e.Data.AgentID, e.Data.Processes
	}
	path := ""
	if e.Data != nil {
		path = e.Data.Path
	}
	return &Error{Code: code, Message: e.Message, AgentID: agent, Processes: procs, Path: path}
}

// MaxLine bounds a control line (hook payloads carry tool output).
const MaxLine = 8 << 20

// Client is a control connection.
type Client struct {
	// Caller (agent tree): when set, Call adds "caller": Caller to the
	// params object of every agents.* method, so hesperd knows which
	// agent acts (hesperctl sets it from HESPER_AGENT_ID).
	Caller string

	conn  net.Conn
	wmu   sync.Mutex
	mu    sync.Mutex
	next  int64
	calls map[string]chan *Response
	notes chan Notification
	done  chan struct{}
	err   error
}

// Dial opens a control connection to the daemon's socket.
func Dial(ctx context.Context, path string) (*Client, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	c := &Client{conn: conn, calls: map[string]chan *Response{}, notes: make(chan Notification, 1024), done: make(chan struct{})}
	go c.read()
	return c, nil
}

func (c *Client) read() {
	r := bufio.NewReaderSize(c.conn, 64<<10)
	var err error
	for {
		var line []byte
		line, err = readLine(r)
		if err != nil {
			break
		}
		var head struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(line, &head) != nil {
			continue
		}
		if head.Method != "" && len(head.ID) == 0 {
			var n Notification
			if json.Unmarshal(line, &n) == nil {
				select {
				case c.notes <- n:
				case <-c.done:
				}
			}
			continue
		}
		var res Response
		if json.Unmarshal(line, &res) != nil {
			continue
		}
		c.mu.Lock()
		ch := c.calls[string(res.ID)]
		delete(c.calls, string(res.ID))
		c.mu.Unlock()
		if ch != nil {
			ch <- &res
		}
	}
	c.mu.Lock()
	c.err = err
	calls := c.calls
	c.calls = map[string]chan *Response{}
	c.mu.Unlock()
	for _, ch := range calls {
		close(ch)
	}
	close(c.notes)
}

// readLine reads one line of at most MaxLine bytes, without the newline.
func readLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > MaxLine {
			return nil, fmt.Errorf("line longer than %d bytes", MaxLine)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			if err == io.EOF && len(line) > 0 {
				return bytes.TrimRight(line, "\r\n"), nil
			}
			return nil, err
		}
		return bytes.TrimRight(line, "\r\n"), nil
	}
}

// ReadLine is readLine for the daemon's side.
func ReadLine(r *bufio.Reader) ([]byte, error) { return readLine(r) }

// Call sends a request and decodes its result into result (when not nil).
// A daemon error comes back as *Error.
func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	c.mu.Lock()
	if c.calls == nil || c.err != nil {
		c.mu.Unlock()
		return errors.New("connection closed")
	}
	c.next++
	id := strconv.FormatInt(c.next, 10)
	ch := make(chan *Response, 1)
	c.calls[id] = ch
	c.mu.Unlock()
	req := Request{JSONRPC: "2.0", ID: json.RawMessage(id), Method: method}
	if params != nil {
		p, err := json.Marshal(params)
		if err != nil {
			return err
		}
		req.Params = withCaller(p, c.Caller, method)
	} else if c.Caller != "" && CarriesCaller(method) {
		req.Params = withCaller(json.RawMessage("{}"), c.Caller, method)
	}
	line, err := json.Marshal(req)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	if deadline, ok := ctx.Deadline(); ok {
		c.conn.SetWriteDeadline(deadline)
	}
	_, err = c.conn.Write(append(line, '\n'))
	c.conn.SetWriteDeadline(time.Time{})
	c.wmu.Unlock()
	if err != nil {
		return err
	}
	select {
	case res, ok := <-ch:
		if !ok {
			return errors.New("connection closed")
		}
		if res.Error != nil {
			return res.Error.Err()
		}
		if result != nil && len(res.Result) > 0 {
			return json.Unmarshal(res.Result, result)
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.calls, id)
		c.mu.Unlock()
		return ctx.Err()
	}
}

// CarriesCaller: the methods whose params carry "caller" (agent tree):
// agents.*, the session starts (sessions.resume, sessions.fork,
// sessions.continueAs) and files.put / files.chunk.
func CarriesCaller(method string) bool {
	switch method {
	case "sessions.resume", "sessions.fork", "sessions.continueAs", "files.put", "files.chunk":
		return true
	}
	return strings.HasPrefix(method, "agents.")
}

// withCaller adds "caller" to the params object of a method that carries
// it (when it has none).
func withCaller(params json.RawMessage, caller, method string) json.RawMessage {
	if caller == "" || !CarriesCaller(method) {
		return params
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(params, &m) != nil || m == nil {
		return params
	}
	if _, ok := m["caller"]; ok {
		return params
	}
	m["caller"], _ = json.Marshal(caller)
	out, err := json.Marshal(m)
	if err != nil {
		return params
	}
	return out
}

// Notifications are the daemon's notifications (agents.subscribe); the
// channel closes with the connection. Read it, or the connection stalls.
func (c *Client) Notifications() <-chan Notification { return c.notes }

// Close closes the connection.
func (c *Client) Close() error {
	select {
	case <-c.done:
	default:
		close(c.done)
	}
	return c.conn.Close()
}

// AttachConn is an attach connection after the daemon accepted it.
type AttachConn struct {
	net.Conn
	Reply AttachReply
	r     *bufio.Reader
	wmu   sync.Mutex
	buf   []byte
}

// Attach opens an attach connection. A refusal comes back as *Error.
func Attach(ctx context.Context, path string, req AttachRequest) (*AttachConn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	line, _ := json.Marshal(req)
	if _, err := conn.Write(append(line, '\n')); err != nil {
		conn.Close()
		return nil, err
	}
	a := &AttachConn{Conn: conn, r: bufio.NewReaderSize(conn, 64<<10)}
	reply, err := readLine(a.r)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if err := json.Unmarshal(reply, &a.Reply); err != nil {
		conn.Close()
		return nil, err
	}
	if !a.Reply.OK {
		conn.Close()
		if a.Reply.Error != nil {
			return nil, a.Reply.Error
		}
		return nil, &Error{Code: CodeInvalid, Message: "attach refused"}
	}
	conn.SetDeadline(time.Time{})
	return a, nil
}

// ReadFrame reads the next frame; the payload is valid until the next call.
func (a *AttachConn) ReadFrame() (byte, []byte, error) {
	typ, p, err := ReadFrame(a.r, a.buf)
	if err == nil && cap(p) > cap(a.buf) {
		a.buf = p[:0]
	}
	return typ, p, err
}

// Input sends terminal input.
func (a *AttachConn) Input(p []byte) error { return a.write(FrameData, p) }

// Resize asks for a new PTY size (applied when this attach is the size owner).
func (a *AttachConn) Resize(cols, rows int) error {
	return a.write(FrameResize, SizePayload(cols, rows))
}

// Scroll moves a view attach's window into the scrollback (FrameScroll).
func (a *AttachConn) Scroll(sc Scroll) error {
	p, _ := json.Marshal(sc)
	return a.write(FrameScroll, p)
}

func (a *AttachConn) write(typ byte, p []byte) error {
	a.wmu.Lock()
	defer a.wmu.Unlock()
	return WriteFrame(a.Conn, typ, p)
}

// SendHook delivers a hook event and waits for the daemon's answer, all
// within timeout. It is what `hesperd hook` does.
func SendHook(path string, p HookParams, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	conn, err := net.DialTimeout("unix", path, timeout)
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetDeadline(deadline)
	params, err := json.Marshal(p)
	if err != nil {
		return err
	}
	line, err := json.Marshal(Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "hook", Params: params})
	if err != nil {
		return err
	}
	if _, err := conn.Write(append(line, '\n')); err != nil {
		return err
	}
	reply, err := readLine(bufio.NewReader(conn))
	if err != nil {
		return err
	}
	var res Response
	if err := json.Unmarshal(reply, &res); err != nil {
		return err
	}
	if res.Error != nil {
		return res.Error.Err()
	}
	return nil
}
