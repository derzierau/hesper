package remote

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"

	"github.com/derzierau/hesper/relay/pkg/client"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Shared history (internal/sessions): the Fleet tells the history when a
// link opens and when a host's change hint arrives on it (it pulls), and
// serves as its way to the other Macs (sessions.Peers: signed requests,
// the hosts' transfer keys, session exports).

// SessionsLinks is what the Fleet tells the shared history.
type SessionsLinks interface {
	LinkUp(machine string)
	HintFrom(machine string, raw json.RawMessage)
}

// SetSessions connects the shared history (before Run).
func (f *Fleet) SetSessions(s SessionsLinks) { f.opt.Sessions = s }

// sessionsLinkUp: a link to m opened (f.mu is held).
func (f *Fleet) sessionsLinkUp(m *machine) {
	if f.opt.Sessions != nil {
		go f.opt.Sessions.LinkUp(m.short)
	}
}

func (f *Fleet) sessionsEvent(l *link, raw json.RawMessage) {
	if f.opt.Sessions == nil {
		return
	}
	f.mu.Lock()
	current, short := l.m.link == l, l.m.short
	f.mu.Unlock()
	if current {
		f.opt.Sessions.HintFrom(short, raw)
	}
}

// Linked are the machines with a link now.
func (f *Fleet) Linked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, m := range f.machines {
		if m.link != nil && m.online {
			out = append(out, m.short)
		}
	}
	return out
}

// TransferKey is a machine's published transfer key.
func (f *Fleet) TransferKey(short string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.machines {
		if m.short == short {
			return m.state.TransferKey
		}
	}
	return ""
}

// Fetch has a machine pack an export (agents.export of id, incremental
// from have) and downloads it into dir, sealed for this Mac. progress
// (optional) gets the bytes received of the export's size (as it
// travels: the conversation compressed); a first call with got 0 once
// packing is done.
func (f *Fleet) Fetch(ctx context.Context, short, id string, have []string, dir string, progress func(got, total int64)) error {
	src, err := f.side(short)
	if err != nil {
		return err
	}
	if src.m == nil {
		return wire.Errorf(wire.CodeInvalid, "%s is this Mac", short)
	}
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	exp, err := src.c.Export(client.RequireE2E(ctx), src.m.id, id, have, key)
	if err != nil {
		return wireError(err)
	}
	if progress != nil {
		progress(0, exp.Size())
	}
	return wireError(src.c.Download(client.RequireE2E(ctx), src.m.id, src.transferKey, exp, key, dir, progress))
}
