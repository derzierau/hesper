package host

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"

	"github.com/derzierau/hesper/relay/pkg/agentlink"
	"github.com/derzierau/hesper/relay/pkg/protocol"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// transcript serves agents.transcript (right observe): a page of an
// agent's transcript as parsed items (the shared history reads it).
func (s *Service) transcript(ctx context.Context, m protocol.Message) (json.RawMessage, error) {
	var p wire.TranscriptParams
	if err := params(m.Params, &p); err != nil {
		return nil, err
	}
	if s.Sessions == nil {
		return nil, protocol.Err("unsupported", "Host keeps no history")
	}
	a, err := s.agent(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	page, err := s.Sessions.Transcript(ctx, a, p.Before, p.Limit)
	if err != nil {
		return nil, publicError(err)
	}
	return protocol.JSON(page), nil
}

// transcriptText serves a bounded chunk of one message under observe.
func (s *Service) transcriptText(ctx context.Context, m protocol.Message) (json.RawMessage, error) {
	var p wire.TranscriptTextParams
	if err := params(m.Params, &p); err != nil {
		return nil, err
	}
	if s.Sessions == nil {
		return nil, protocol.Err("unsupported", "Host keeps no history")
	}
	a, err := s.agent(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	chunk, err := s.Sessions.TranscriptText(ctx, a, p.Seq, p.Offset, p.Limit)
	if err != nil {
		return nil, publicError(err)
	}
	return protocol.JSON(chunk), nil
}

// transcriptStream serves agents.transcriptStream (right observe): the
// transcript's items as JSON lines on a channel of the caller's link,
// registered like an attach's. The controller's X ends it; the host
// closes the channel when the link ends, the agent is removed or gets
// another session id, or the follow fails. What the controller sends on
// the channel is dropped.
func (s *Service) transcriptStream(ctx context.Context, m protocol.Message) (json.RawMessage, error) {
	var p wire.TranscriptStreamParams
	if err := params(m.Params, &p); err != nil {
		return nil, err
	}
	if p.Ch == 0 {
		return nil, protocol.Err("invalid_request", "Invalid channel")
	}
	if s.Sessions == nil {
		return nil, protocol.Err("unsupported", "Host keeps no history")
	}
	link := s.links.get(p.Link)
	if link == nil || link.device != m.ControllerID {
		return nil, protocol.Err("not_found", "No such link")
	}
	// Subscribed before the agent is read, so a new session id (/clear)
	// or a removal after the read is seen; the goroutine below owns sub
	// once the channel is registered.
	sub := s.Agents.Subscribe()
	owned := false
	defer func() {
		if !owned {
			s.Agents.Unsubscribe(sub)
		}
	}()
	a, err := s.agent(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	// A shell, or no transcript yet: the reply says so, not an X.
	if _, err := s.Sessions.TranscriptPath(a); err != nil {
		return nil, publicError(err)
	}
	// closeChan closes a channel's pipe; this channel has no terminal
	// behind it, so the pipe is never read.
	near, far := net.Pipe()
	near.Close()
	c := &hostChan{in: make(chan []byte, chanQueue), done: make(chan struct{}), pipe: far}
	link.mu.Lock()
	switch {
	case link.ctx.Err() != nil:
		link.mu.Unlock()
		return nil, protocol.Err("not_found", "No such link")
	case link.chans[p.Ch] != nil || len(link.chans) >= maxChannels:
		link.mu.Unlock()
		return nil, protocol.Err("invalid_request", "Channel in use")
	}
	link.chans[p.Ch] = c
	link.mu.Unlock()
	follow, stop := context.WithCancel(link.ctx)
	owned = true
	go func() {
		// The channel's end: the controller's X, the agent's removal or
		// new session.
		defer s.Agents.Unsubscribe(sub)
		defer stop()
		ended := make(chan struct{})
		go func() {
			defer close(ended)
			for {
				notes := sub.Wait(follow.Done())
				if len(notes) == 0 && follow.Err() != nil {
					return
				}
				for _, n := range notes {
					// A new session id (/clear, a resume) is another
					// transcript: the phone opens a stream on it.
					if n.Removed == a.ID || n.Agent != nil && n.Agent.ID == a.ID && n.Agent.SessionID != a.SessionID {
						return
					}
				}
			}
		}()
		select {
		case <-c.done:
		case <-ended:
		case <-follow.Done():
		}
	}()
	go func() {
		// Input on the channel means nothing here: drop it, so a full
		// queue never ends the channel.
		for {
			select {
			case <-c.in:
			case <-c.done:
				return
			}
		}
	}()
	go func() {
		defer stop()
		err := s.Sessions.FollowTranscript(follow, a, p.After, p.Limit, func(it wire.TranscriptItem) error {
			line, err := json.Marshal(it)
			if err != nil {
				return err
			}
			if err := link.conn.Write(link.ctx, agentlink.KindData, p.Ch, append(line, '\n')); err != nil {
				link.cancel()
				return err
			}
			return nil
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("transcript stream ended", "agent", a.ID, "error", err)
		}
		select {
		case <-c.done:
			// The controller closed it.
		default:
			link.closeChan(p.Ch, true)
		}
	}()
	return protocol.JSON(struct{}{}), nil
}
