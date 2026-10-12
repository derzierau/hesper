package agents

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/internal/routines"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

func TestRoutinesSaveListPauseRunsRemoveAndRestart(t *testing.T) {
	h := newHarness(t)
	var saved wire.Routine
	if err := h.call("routines.save", wire.RoutineSaveParams{Routine: wire.Routine{Name: "Tidy", Every: "1h", Offset: "5m",
		Spawn: wire.SpawnParams{Task: "tidy the tests", Project: h.project, Name: "ignored"}}}, &saved); err != nil {
		t.Fatal(err)
	}
	if !routines.ID.MatchString(saved.ID) || saved.Name != "Tidy" || saved.CreatedBy != "local" || saved.Created.IsZero() || saved.NextRun.IsZero() ||
		saved.Policy.Timeout != "1h" || saved.Policy.Overlap != wire.OverlapSkip || saved.Policy.CatchUp != wire.CatchUpOnce || saved.Policy.KeepRuns != routines.DefaultKeepRuns ||
		saved.Spawn.Name != "" || saved.Machine != "" {
		t.Fatalf("saved %+v", saved)
	}
	var list []wire.Routine
	if err := h.call("routines.list", nil, &list); err != nil || len(list) != 1 || list[0].ID != saved.ID {
		t.Fatalf("list %+v %v", list, err)
	}
	// Pause: NextRun zero; resume: back.
	var paused wire.Routine
	if err := h.call("routines.pause", wire.RoutinePauseParams{ID: saved.ID, Paused: true}, &paused); err != nil || !paused.Paused || !paused.NextRun.IsZero() {
		t.Fatalf("paused %+v %v", paused, err)
	}
	var resumed wire.Routine // paused is omitempty: decode into a fresh value
	if err := h.call("routines.pause", wire.RoutinePauseParams{ID: saved.ID}, &resumed); err != nil || resumed.Paused || resumed.NextRun.IsZero() {
		t.Fatalf("resumed %+v %v", resumed, err)
	}
	var runs []wire.RoutineRun
	if err := h.call("routines.runs", wire.RoutineRunsParams{ID: saved.ID}, &runs); err != nil || len(runs) != 0 {
		t.Fatalf("runs %+v %v", runs, err)
	}
	// This machine by name is fine; another machine is not available yet.
	if err := h.call("routines.save", wire.RoutineSaveParams{Routine: wire.Routine{ID: "r-here", Machine: "L", At: []string{"09:00"},
		Spawn: wire.SpawnParams{Task: "report", Project: h.project}}}, nil); err != nil {
		t.Fatal(err)
	}
	check := func(method string, params any, code string) {
		t.Helper()
		err := h.call(method, params, nil)
		if we, ok := err.(*wire.Error); !ok || we.Code != code {
			t.Fatalf("%s: %v, want %s", method, err, code)
		}
	}
	check("routines.save", wire.RoutineSaveParams{Routine: wire.Routine{Machine: "M", Every: "1h", Spawn: wire.SpawnParams{Task: "x", Project: h.project}}}, wire.CodeUnavailable)
	check("routines.save", wire.RoutineSaveParams{Routine: wire.Routine{ID: "d-x", Every: "1h", Spawn: wire.SpawnParams{Task: "x", Project: h.project}}}, wire.CodeInvalid)
	check("routines.save", wire.RoutineSaveParams{Routine: wire.Routine{Every: "1h", Spawn: wire.SpawnParams{Task: "x", Project: "rel"}}}, wire.CodeInvalid)
	check("routines.remove", wire.IDParams{ID: "r-nope"}, wire.CodeNotFound)
	check("routines.remove", wire.IDParams{}, wire.CodeInvalid)
	check("routines.pause", wire.RoutinePauseParams{ID: "r-nope", Paused: true}, wire.CodeNotFound)
	check("routines.pause", wire.RoutinePauseParams{}, wire.CodeInvalid)
	check("routines.runs", wire.RoutineRunsParams{ID: "r-nope"}, wire.CodeNotFound)
	check("routines.runs", wire.RoutineRunsParams{}, wire.CodeInvalid)

	// The file is 0600 and survives a daemon restart.
	st, err := os.Stat(filepath.Join(h.state, "routines.json"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("routines.json %v %v", st, err)
	}
	h.close()
	h.open()
	if err := h.call("routines.list", nil, &list); err != nil || len(list) != 2 || list[0].ID != saved.ID || list[0].Name != "Tidy" || list[0].NextRun.IsZero() || list[1].ID != "r-here" {
		t.Fatalf("after restart %+v %v", list, err)
	}
	if err := h.call("routines.remove", wire.IDParams{ID: "r-here"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := h.call("routines.list", nil, &list); err != nil || len(list) != 1 {
		t.Fatalf("after remove %+v %v", list, err)
	}
}

func TestRoutinesOnSubscribe(t *testing.T) {
	h := newHarness(t)
	if err := h.call("routines.save", wire.RoutineSaveParams{Routine: wire.Routine{ID: "r-one", Every: "1h", Spawn: wire.SpawnParams{Task: "one", Project: h.project}}}, nil); err != nil {
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
	var ch wire.RoutineChanged
	json.Unmarshal(next(wire.NoteRoutineChanged), &ch)
	if ch.Routine.ID != "r-one" || ch.Routine.Spawn.Task != "one" {
		t.Fatalf("first %+v", ch)
	}
	if err := h.call("routines.save", wire.RoutineSaveParams{Routine: wire.Routine{ID: "r-one", Every: "2h", Spawn: wire.SpawnParams{Task: "one two", Project: h.project}}}, nil); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(next(wire.NoteRoutineChanged), &ch)
	if ch.Routine.Spawn.Task != "one two" || ch.Routine.Every != "2h" {
		t.Fatalf("changed %+v", ch)
	}
	if err := h.call("routines.remove", wire.IDParams{ID: "r-one"}, nil); err != nil {
		t.Fatal(err)
	}
	var rm wire.Removed
	json.Unmarshal(next(wire.NoteRoutineRemoved), &rm)
	if rm.ID != "r-one" {
		t.Fatalf("removed %+v", rm)
	}
}
