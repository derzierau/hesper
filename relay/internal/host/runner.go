package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/internal/perf"
	"github.com/derzierau/hesper/relay/pkg/client"
	"github.com/derzierau/hesper/relay/pkg/e2e"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

// EventSettle is how long the host gathers a burst of change events before
// it publishes; PublishGap is the least time between two event-driven
// publications. A snapshot of a busy host takes a few milliseconds, so a
// change reaches the relay about EventSettle plus that after its event.
var (
	EventSettle = 15 * time.Millisecond
	PublishGap  = 50 * time.Millisecond
)

type Runner struct {
	Events          <-chan struct{}
	Service         *Service
	Credentials     protocol.Credentials
	CredentialsPath string
	PollInterval    time.Duration
	Logger          *slog.Logger
	// Auth checks every request's device-key signature (Part K), once per
	// request, and puts the caller into the operation's context (CallerFrom).
	// hesperd always sets it; nil (tests of other parts only) skips the
	// checks, and with no caller every shell operation is refused.
	Auth *Authorizer
	// E2E (part N) answers handshakes and opens requests sent through the
	// end-to-end channel; with E2E.Require plaintext is refused for all but
	// observing. nil: no channel (requests arrive in plaintext only).
	E2E *E2E
	// Direct (optional) is the direct path's listener (direct.go): Run
	// starts it, the channel hands out its addresses (direct.offer) and its
	// requests run here, one at a time with the relay's.
	Direct *Direct

	// opMu serializes operations from the relay and the direct path, so
	// two controllers cannot interleave paste and submit.
	opMu      sync.Mutex
	setup     sync.Once
	termSlots chan struct{} // links of both paths
	changed   chan struct{} // an operation changed published state
}

func (r *Runner) init() {
	r.setup.Do(func() {
		r.termSlots = make(chan struct{}, 16)
		r.changed = make(chan struct{}, 1)
	})
}

// republish asks the relay loop to publish a fresh snapshot.
func (r *Runner) republish() {
	r.init()
	select {
	case r.changed <- struct{}{}:
	default:
	}
}

// publishAfter: methods whose effect shows in the published snapshot
// (agent changes also arrive through Events; this saves the debounce).
func publishAfter(method string) bool {
	switch method {
	case "agents.spawn", "agents.stop", "agents.remove", "agents.resume", "agents.answer",
		"agents.close", "agents.kill", "agents.background":
		return true
	}
	return false
}

// perform authorizes and runs one request (part K's check exactly once),
// serialized with every other operation. opCtx says how it came (the
// channel, the direct path); terminals opens its links.
func (r *Runner) perform(opCtx context.Context, m protocol.Message, terminals *terminalManager) (json.RawMessage, error) {
	r.opMu.Lock()
	defer r.opMu.Unlock()
	var result json.RawMessage
	var err error
	switch {
	case m.Method == "devices.request":
		if r.Auth == nil {
			return nil, protocol.Err("unsupported", "Host does not approve devices")
		}
		return r.Auth.RequestApproval(opCtx, m)
	case r.Auth != nil:
		var caller Caller
		started := time.Now()
		caller, err = r.Auth.Authorize(opCtx, m)
		perf.Host.Since(perf.RPCAuthorize, started)
		if err != nil {
			return nil, err
		}
		opCtx = WithCaller(opCtx, caller)
	}
	started := time.Now()
	if m.Method == "agents.link" {
		result, err = terminals.openLink(opCtx, m)
	} else {
		result, err = r.Service.Execute(opCtx, m)
	}
	if perf.Host.Enabled() {
		perf.Host.Since(perf.RPCExecute+" "+m.Method, started)
	}
	return result, err
}

func (r *Runner) Run(ctx context.Context) error {
	if r.Credentials.Role != "host" {
		return protocol.Err("forbidden", "Host credentials required")
	}
	if r.PollInterval < time.Second {
		r.PollInterval = 5 * time.Second
	}
	if r.Logger == nil {
		r.Logger = slog.Default()
	}
	if r.Direct != nil {
		if err := r.Direct.Start(ctx, r); err != nil {
			r.Logger.Warn("direct path unavailable; everything goes through the relay", "error", err)
		}
	}
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		if r.CredentialsPath != "" {
			// Only the relay's explicit rejection ends the host; every other
			// renewal failure keeps the stored credential and retries below.
			c, err := client.FreshCredentials(ctx, r.CredentialsPath)
			if errors.Is(err, client.ErrSignInRequired) {
				return err
			}
			if err == nil || errors.Is(err, client.ErrRenewalPostponed) {
				r.Credentials = c
			}
			if err != nil && ctx.Err() == nil {
				if errors.Is(err, client.ErrRenewalPostponed) {
					r.Logger.Warn("credential renewal postponed", "error", err)
				} else {
					r.Logger.Warn("cannot read credentials; retrying", "error", err)
				}
			}
		}
		var link *client.Link
		var err error
		if time.Until(r.Credentials.ExpiresAt) > 0 || r.Credentials.ExpiresAt.IsZero() {
			link, err = client.Dial(ctx, r.Credentials)
		} else {
			err = errors.New("waiting for the relay to renew credentials")
		}
		if err == nil {
			r.Logger.Info("host connected", "device", r.Credentials.DeviceID)
			err = r.Serve(ctx, link)
			link.Close()
		}
		var fault *protocol.Error
		if errors.As(err, &fault) && fault.Code == "unauthorized" {
			if r.CredentialsPath != "" {
				return client.SignInRequired(r.CredentialsPath, r.Credentials, err)
			}
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
		r.Logger.Warn("host disconnected; reconnecting", "error", err)
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		timer := time.NewTimer(backoff + time.Duration(rand.Int64N(int64(backoff/2))))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		backoff = min(backoff*2, 30*time.Second)
	}
	return nil
}

// openE2E opens one sealed request (timed for the latency harness).
func (r *Runner) openE2E(m protocol.Message) (protocol.Message, *hostSession, string, error) {
	started := time.Now()
	defer perf.Host.Since(perf.RPCOpen, started)
	return r.E2E.Open(m)
}

// Serve executes one operation at a time. The reader remains active for protocol
// heartbeats; bounded queues and request deadlines prevent unbounded stale work.
func (r *Runner) Serve(parent context.Context, link *client.Link) error {
	r.init()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	defer link.Close()
	terminals := &terminalManager{ctx: ctx, r: r, slots: r.termSlots}
	defer func() { cancel(); terminals.wg.Wait() }()
	requests := make(chan protocol.Message, 16)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		defer cancel()
		for {
			m, err := link.Read(ctx)
			if err != nil {
				return
			}
			if m.Type != "request" {
				continue
			}
			select {
			case requests <- m:
			default:
				return
			}
		}
	}()
	defer func() { cancel(); <-readDone }()
	interval := r.PollInterval
	if interval < time.Second {
		interval = 5 * time.Second
	}
	poll := time.NewTicker(interval)
	defer poll.Stop()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	var previous []byte
	var published time.Time
	publish := func() error {
		published = time.Now()
		state, err := r.Service.Snapshot(ctx)
		var data []byte
		if err != nil {
			data = protocol.JSON(map[string]any{"error": protocol.PublicError(err)})
		} else {
			data = protocol.JSON(state)
		}
		if bytes.Equal(previous, data) {
			return nil
		}
		if err := link.Send(ctx, protocol.Message{Type: "snapshot", Result: data}); err != nil {
			return err
		}
		previous = data
		return nil
	}
	if err := publish(); err != nil {
		return err
	}
	var debounce <-chan time.Time
	var eventTimer *time.Timer
	defer func() {
		if eventTimer != nil {
			eventTimer.Stop()
		}
	}()
	seen := map[string]bool{}
	var order []string
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-r.Events:
			// A change (a Hesper hook, a branch or presence read in the
			// background) is published at once: after EventSettle, which
			// gathers a burst, and no sooner than PublishGap after the last
			// publication.
			if debounce == nil {
				eventTimer = time.NewTimer(max(EventSettle, PublishGap-time.Since(published)))
				debounce = eventTimer.C
			}
		case <-debounce:
			debounce = nil
			if err := publish(); err != nil {
				return err
			}
		case <-poll.C:
			if err := publish(); err != nil {
				return err
			}
		case <-ping.C:
			if err := link.Ping(ctx); err != nil {
				return err
			}
		case m := <-requests:
			if seen[m.ID] {
				return protocol.Err("duplicate_request", "Relay repeated a request ID")
			}
			seen[m.ID] = true
			order = append(order, m.ID)
			if len(order) > 1024 {
				delete(seen, order[0])
				order = order[1:]
			}
			var result []byte
			var err error
			opCtx := ctx
			// The end-to-end channel (part N): a handshake is answered
			// here; a frame is opened into the inner request, which then
			// runs like any other and is answered sealed.
			var via *hostSession
			innerID := ""
			if m.Method == e2e.HelloMethod || m.Method == e2e.FrameMethod {
				if r.E2E == nil {
					err = protocol.Err("unsupported", "Host has no end-to-end channel")
				} else if m.Method == e2e.HelloMethod {
					result, err = r.E2E.Hello(m)
				} else if inner, s, id, e := r.openE2E(m); e != nil {
					err = e
				} else {
					m, via, innerID = inner, s, id
					opCtx = withE2E(ctx, s)
				}
				if via == nil {
					response := protocol.Message{Type: "result", ID: m.ID, Result: result}
					if err != nil {
						response.Result, response.Error = nil, protocol.PublicError(err)
					}
					if err := link.Send(ctx, response); err != nil {
						return err
					}
					continue
				}
			}
			if via == nil && r.E2E != nil && r.E2E.Require && r.E2E.Auth != nil && requiresE2E(m) {
				// Nothing but the relay-visible snapshot and pings in
				// plaintext: agents' names, tasks and screens, typing,
				// answers and moves only through the channel, whatever the
				// signature says.
				r.E2E.Auth.Audit("request.refused", map[string]any{"device": m.ControllerID, "method": m.Method, "ok": false, "e2e": false, "route": "relay",
					"detail": "plaintext refused (--require-e2e)"})
				err = protocol.Err(e2e.CodeRequired, "This host accepts "+m.Method+" only through the end-to-end channel")
			}
			if err == nil && m.Method == "direct.offer" {
				// The direct path's addresses, only inside the channel.
				result, err = r.offer(via)
			} else if err == nil {
				result, err = r.perform(opCtx, m, terminals)
			}
			response := protocol.Message{Type: "result", ID: m.ID, Result: result}
			if via != nil {
				started := time.Now()
				response.Result = r.E2E.Seal(via, innerID, result, err)
				perf.Host.Since(perf.RPCSeal, started)
			} else if err != nil {
				response.Result = nil
				response.Error = protocol.PublicError(err)
			}
			if err := link.Send(ctx, response); err != nil {
				return err
			}
			if publishAfter(m.Method) {
				if err := publish(); err != nil {
					return err
				}
			}
		case <-r.changed:
			// A request on the direct path changed what snapshots show.
			if err := publish(); err != nil {
				return err
			}
		}
	}
}
