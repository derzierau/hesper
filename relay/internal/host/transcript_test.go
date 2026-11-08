package host

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/derzierau/hesper/relay/pkg/agentlink"
	"github.com/derzierau/hesper/relay/pkg/protocol"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// fakeTranscripts is a SessionsHost whose follow sends one item, then
// waits for its ctx (sent on ended).
type fakeTranscripts struct {
	ended chan struct{}
}

func (f *fakeTranscripts) HostCall(context.Context, string, json.RawMessage) (any, error) {
	return nil, nil
}
func (f *fakeTranscripts) HostHint() json.RawMessage  { return nil }
func (f *fakeTranscripts) Changes() <-chan struct{}   { return nil }
func (f *fakeTranscripts) Exportable(id string) error { return nil }
func (f *fakeTranscripts) Transcript(_ context.Context, a wire.Agent, before int64, limit int) (wire.TranscriptPage, error) {
	return wire.TranscriptPage{Items: []wire.TranscriptItem{{Seq: 1, Kind: wire.ItemUser, Text: a.ID}}}, nil
}
func (f *fakeTranscripts) FollowTranscript(ctx context.Context, a wire.Agent, after int64, limit int, emit func(wire.TranscriptItem) error) error {
	if err := emit(wire.TranscriptItem{Seq: 7, Kind: wire.ItemAssistant, Text: "hi"}); err != nil {
		return err
	}
	<-ctx.Done()
	f.ended <- struct{}{}
	return ctx.Err()
}

// frames is a link stream that records what the host writes.
type frames struct {
	mu  sync.Mutex
	got [][]byte
}

func (f *frames) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	<-ctx.Done()
	return 0, nil, ctx.Err()
}
func (f *frames) Write(_ context.Context, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, bytes.Clone(data))
	return nil
}
func (f *frames) Close() error { return nil }

func (f *frames) has(match func([]byte) bool) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, g := range f.got {
		if match(g) {
			return true
		}
	}
	return false
}

func TestTranscriptStreamEnds(t *testing.T) {
	reg, project := testRegistry(t)
	fake := &fakeTranscripts{ended: make(chan struct{}, 4)}
	s := &Service{Agents: reg, Sessions: fake}
	a, err := reg.Spawn(wire.SpawnParams{Project: project, Task: "t"})
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Stop(a.ID)
	stream := &frames{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	link := &hostLink{id: "L1", device: "dev", conn: agentlink.New(stream), ctx: ctx, cancel: cancel, opened: time.Now(), chans: map[uint32]*hostChan{}}
	s.links.add(link)
	open := func(ch uint32) error {
		params, _ := json.Marshal(wire.TranscriptStreamParams{Link: "L1", Ch: ch, ID: a.ID})
		_, err := s.Execute(context.Background(), protocol.Message{Method: "agents.transcriptStream", Params: params, ControllerID: "dev", Deadline: time.Now().Add(time.Second).UnixMilli()})
		return err
	}
	ended := func(what string) {
		t.Helper()
		select {
		case <-fake.ended:
		case <-time.After(time.Second):
			t.Fatalf("%s: the follow goes on", what)
		}
	}
	gone := func(ch uint32) bool {
		link.mu.Lock()
		defer link.mu.Unlock()
		return link.chans[ch] == nil
	}

	if err := open(1); err != nil {
		t.Fatal(err)
	}
	if err := open(1); code(err) != "invalid_request" {
		t.Fatalf("second stream on channel 1: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for !stream.has(func(b []byte) bool { return bytes.Contains(b, []byte(`{"seq":7,"kind":"assistant","text":"hi"}`+"\n")) }) {
		if time.Now().After(deadline) {
			t.Fatalf("no item on the channel: %q", stream.got)
		}
		time.Sleep(10 * time.Millisecond)
	}
	link.input(1, []byte("ignored")) // the controller's bytes are dropped
	link.closeChan(1, false)         // the controller's X
	ended("X")
	if !gone(1) {
		t.Fatal("channel 1 still registered")
	}

	// The agent's removal ends it too, and the host sends X.
	if err := open(2); err != nil {
		t.Fatal(err)
	}
	reg.Stop(a.ID)
	deadline = time.Now().Add(5 * time.Second)
	for reg.Remove(a.ID) != nil {
		if time.Now().After(deadline) {
			t.Fatal("agent not removable")
		}
		time.Sleep(20 * time.Millisecond)
	}
	ended("removed")
	deadline = time.Now().Add(time.Second)
	for !gone(2) || !stream.has(func(b []byte) bool { return len(b) > 0 && b[0] == agentlink.KindClose }) {
		if time.Now().After(deadline) {
			t.Fatalf("no X after the removal: %q", stream.got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
