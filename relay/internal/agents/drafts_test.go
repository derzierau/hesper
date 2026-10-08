package agents

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

func TestDraftsSaveListRemoveAndRestart(t *testing.T) {
	h := newHarness(t)
	tr := true
	var saved wire.Draft
	if err := h.call("drafts.save", wire.DraftSaveParams{Draft: wire.Draft{ID: "d-abc", Text: "Fix the flaky badge test\n@mini /codex", Machine: "M",
		Project: h.project, Profile: "codex-full", Worktree: &tr, Branch: "fix/badge", Attachments: []string{"/tmp/a.png"}, After: "L/a7f3k2",
		Band: "p-gh", Wall: "wall-2b", ProjectLocked: true}}, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.ID != "d-abc" || saved.Created.IsZero() || saved.Updated.IsZero() || saved.Machine != "M" || saved.Worktree == nil || !*saved.Worktree {
		t.Fatalf("saved %+v", saved)
	}
	// Save again: Created stays, Updated moves.
	time.Sleep(5 * time.Millisecond)
	var again wire.Draft
	saved.Text += " more"
	saved.Parked = true
	if err := h.call("drafts.save", wire.DraftSaveParams{Draft: saved}, &again); err != nil {
		t.Fatal(err)
	}
	if !again.Created.Equal(saved.Created) || !again.Updated.After(saved.Updated) || !again.Parked {
		t.Fatalf("resave %+v", again)
	}
	// No id: the daemon picks one.
	var auto wire.Draft
	if err := h.call("drafts.save", wire.DraftSaveParams{Draft: wire.Draft{Text: "second"}}, &auto); err != nil || !draftID.MatchString(auto.ID) {
		t.Fatalf("auto id %+v %v", auto, err)
	}
	check := func(method string, params any, code string) {
		t.Helper()
		err := h.call(method, params, nil)
		if we, ok := err.(*wire.Error); !ok || we.Code != code {
			t.Fatalf("%s: %v, want %s", method, err, code)
		}
	}
	check("drafts.save", wire.DraftSaveParams{Draft: wire.Draft{ID: "../x"}}, wire.CodeInvalid)
	check("drafts.remove", wire.IDParams{ID: "d-nope"}, wire.CodeNotFound)
	check("drafts.remove", wire.IDParams{}, wire.CodeInvalid)

	// The file is 0600 and survives a daemon restart.
	st, err := os.Stat(filepath.Join(h.state, "drafts.json"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("drafts.json %v %v", st, err)
	}
	h.close()
	h.open()
	var list []wire.Draft
	if err := h.call("drafts.list", nil, &list); err != nil || len(list) != 2 || list[0].ID != "d-abc" || list[0].Text != again.Text ||
		list[0].After != "L/a7f3k2" || len(list[0].Attachments) != 1 || !list[0].Parked ||
		list[0].Band != "p-gh" || list[0].Wall != "wall-2b" || !list[0].ProjectLocked {
		t.Fatalf("after restart %+v %v", list, err)
	}
	if err := h.call("drafts.remove", wire.IDParams{ID: auto.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if err := h.call("drafts.list", nil, &list); err != nil || len(list) != 1 {
		t.Fatalf("after remove %+v %v", list, err)
	}
}

func TestDraftsOnSubscribe(t *testing.T) {
	h := newHarness(t)
	if err := h.call("drafts.save", wire.DraftSaveParams{Draft: wire.Draft{ID: "d-one", Text: "one"}}, nil); err != nil {
		t.Fatal(err)
	}
	c := h.client()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Call(ctx, "agents.subscribe", nil, nil); err != nil {
		t.Fatal(err)
	}
	next := func(method string) json.RawMessage {
		t.Helper()
		for {
			select {
			case n := <-c.Notifications():
				if n.Method == method {
					return n.Params
				}
			case <-ctx.Done():
				t.Fatalf("no %s", method)
			}
		}
	}
	var ch wire.DraftChanged
	json.Unmarshal(next(wire.NoteDraftChanged), &ch)
	if ch.Draft.ID != "d-one" || ch.Draft.Text != "one" {
		t.Fatalf("first %+v", ch)
	}
	if err := h.call("drafts.save", wire.DraftSaveParams{Draft: wire.Draft{ID: "d-one", Text: "one two"}}, nil); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(next(wire.NoteDraftChanged), &ch)
	if ch.Draft.Text != "one two" {
		t.Fatalf("changed %+v", ch)
	}
	if err := h.call("drafts.remove", wire.IDParams{ID: "d-one"}, nil); err != nil {
		t.Fatal(err)
	}
	var rm wire.Removed
	json.Unmarshal(next(wire.NoteDraftRemoved), &rm)
	if rm.ID != "d-one" {
		t.Fatalf("removed %+v", rm)
	}
}

func TestDraftsBrokenFileStartsEmpty(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "drafts.json"), []byte("{nope"), 0o600)
	s := OpenDrafts(dir, t.Logf)
	if len(s.List()) != 0 {
		t.Fatal("want empty")
	}
	if _, err := os.Stat(filepath.Join(dir, "drafts.json.broken")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(wire.Draft{ID: "d-x", Text: string(make([]byte, draftTextLimit+1))}); err == nil {
		t.Fatal("oversized text saved")
	}
}
