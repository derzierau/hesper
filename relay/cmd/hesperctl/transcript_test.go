package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// fakeHistory is a shared history whose transcripts are two items.
type fakeHistory struct{}

func (fakeHistory) Call(string, json.RawMessage) (any, error, bool) { return nil, nil, false }
func (fakeHistory) Watch(done <-chan struct{}, _ func(string, any) error) {
	<-done
}
func (fakeHistory) Transcript(_ context.Context, a wire.Agent, before int64, limit int) (wire.TranscriptPage, error) {
	return wire.TranscriptPage{Items: []wire.TranscriptItem{
		{Seq: 0, Kind: wire.ItemUser, Text: "fix " + a.ID},
		{Seq: 40, Kind: wire.ItemTool, Tool: "Bash", Summary: "go test"},
		{Seq: 41, Kind: wire.ItemResult, Text: "ok\nmore"},
	}, More: before == 0}, nil
}

func TestTranscriptCommand(t *testing.T) {
	reg, sock, project := daemonSessions(t, nil, fakeHistory{})
	w := &moreWorld{t, reg, sock, project}
	a := w.spawn("t")
	var page wire.TranscriptPage
	if err := json.Unmarshal([]byte(w.out("transcript", a.ID, "--json")), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 3 || page.Items[0].Text != "fix "+a.ID || !page.More {
		t.Fatalf("transcript --json %+v", page)
	}
	if out := w.out("transcript", a.ID); out != "› fix "+a.ID+"\n⏺ Bash go test\n⎿ ok…\n" {
		t.Fatalf("transcript:\n%s", out)
	}
	if code := w.code("transcript", a.ID, "--limit", "5000"); code == 0 {
		t.Fatal("--limit 5000 passed")
	}
}
