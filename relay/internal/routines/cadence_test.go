package routines

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

func valid() wire.Routine {
	return wire.Routine{ID: "r-nightly", Every: "1h", Spawn: wire.SpawnParams{Task: "tidy", Project: "/srv/app"}}
}

func TestValidateDefaults(t *testing.T) {
	r := valid()
	r.Spawn.Name, r.Spawn.Caller, r.Spawn.Parent, r.Spawn.Depth, r.Spawn.Draft = "x", "L/a", "L/b", 2, "d-1"
	r.Spawn.Bring = &wire.Bring{}
	if err := Validate(&r); err != nil {
		t.Fatal(err)
	}
	if r.Name != "nightly" || r.Offset != "0s" || r.Every != "1h" {
		t.Fatalf("cadence %+v", r)
	}
	if p := r.Policy; p.Timeout != "1h" || p.Overlap != wire.OverlapSkip || p.CatchUp != wire.CatchUpOnce || p.KeepRuns != DefaultKeepRuns {
		t.Fatalf("policy %+v", p)
	}
	if sp := r.Spawn; sp.Name != "" || sp.Caller != "" || sp.Parent != "" || sp.Depth != 0 || sp.Bring != nil || sp.Draft != "" {
		t.Fatalf("spawn %+v", sp)
	}
	// At: sorted, deduplicated; the user's duration text is kept.
	r = valid()
	r.Every, r.At, r.Name = "", []string{"18:00", "07:30", "18:00"}, "  Report "
	r.Policy = wire.RunPolicy{Timeout: "90m", Overlap: wire.OverlapReplace, CatchUp: wire.CatchUpNone, KeepRuns: 5}
	if err := Validate(&r); err != nil {
		t.Fatal(err)
	}
	if strings.Join(r.At, ",") != "07:30,18:00" || r.Name != "Report" || r.Offset != "" || r.Policy.Timeout != "90m" || r.Policy.KeepRuns != 5 {
		t.Fatalf("at %+v", r)
	}
	// An empty id passes (the store assigns one); the name is then required.
	r = valid()
	r.ID = ""
	if err := Validate(&r); err == nil || !strings.Contains(err.Error(), "name") {
		t.Fatalf("no id, no name: %v", err)
	}
	r.Name = "n"
	if err := Validate(&r); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]func(r *wire.Routine){
		"bad id":          func(r *wire.Routine) { r.ID = "d-x" },
		"no cadence":      func(r *wire.Routine) { r.Every = "" },
		"both":            func(r *wire.Routine) { r.At = []string{"09:00"} },
		"every text":      func(r *wire.Routine) { r.Every = "hourly" },
		"every short":     func(r *wire.Routine) { r.Every = "30s" },
		"every long":      func(r *wire.Routine) { r.Every = "169h" },
		"offset text":     func(r *wire.Routine) { r.Offset = "five" },
		"offset negative": func(r *wire.Routine) { r.Offset = "-1m" },
		"offset >= every": func(r *wire.Routine) { r.Offset = "1h" },
		"offset with at":  func(r *wire.Routine) { r.Every, r.At, r.Offset = "", []string{"09:00"}, "5m" },
		"at 12h":          func(r *wire.Routine) { r.Every, r.At = "", []string{"9:00"} },
		"at hour":         func(r *wire.Routine) { r.Every, r.At = "", []string{"24:00"} },
		"at minute":       func(r *wire.Routine) { r.Every, r.At = "", []string{"09:60"} },
		"no task":         func(r *wire.Routine) { r.Spawn.Task = " " },
		"task size":       func(r *wire.Routine) { r.Spawn.Task = strings.Repeat("x", TaskLimit+1) },
		"no project":      func(r *wire.Routine) { r.Spawn.Project = "" },
		"relative":        func(r *wire.Routine) { r.Spawn.Project = "app" },
		"spawn machine":   func(r *wire.Routine) { r.Spawn.Machine = "M" },
		"kind":            func(r *wire.Routine) { r.Spawn.Kind = "gemini" },
		"timeout text":    func(r *wire.Routine) { r.Policy.Timeout = "soon" },
		"timeout short":   func(r *wire.Routine) { r.Policy.Timeout = "10s" },
		"timeout long":    func(r *wire.Routine) { r.Policy.Timeout = "25h" },
		"overlap":         func(r *wire.Routine) { r.Policy.Overlap = "queue" },
		"catchUp":         func(r *wire.Routine) { r.Policy.CatchUp = "all" },
		"keepRuns neg":    func(r *wire.Routine) { r.Policy.KeepRuns = -1 },
		"keepRuns max":    func(r *wire.Routine) { r.Policy.KeepRuns = MaxKeepRuns + 1 },
	}
	for name, change := range cases {
		r := valid()
		change(&r)
		err := Validate(&r)
		var we *wire.Error
		if !errors.As(err, &we) || we.Code != wire.CodeInvalid {
			t.Errorf("%s: %v, want invalid", name, err)
		}
	}
	// The passing neighbours of the limits.
	for name, change := range map[string]func(r *wire.Routine){
		"every min":     func(r *wire.Routine) { r.Every = "1m" },
		"every max":     func(r *wire.Routine) { r.Every = "168h" },
		"offset max":    func(r *wire.Routine) { r.Offset = "59m" },
		"machine":       func(r *wire.Routine) { r.Machine, r.Spawn.Machine = "M", "M" },
		"kind":          func(r *wire.Routine) { r.Spawn.Kind = wire.KindCodex },
		"timeout edges": func(r *wire.Routine) { r.Policy.Timeout = "24h" },
		"keepRuns max":  func(r *wire.Routine) { r.Policy.KeepRuns = MaxKeepRuns },
	} {
		r := valid()
		change(&r)
		if err := Validate(&r); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func at(t *testing.T, loc *time.Location, s string) time.Time {
	t.Helper()
	v, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestNextEvery(t *testing.T) {
	for _, tc := range []struct {
		every, offset, after, want string
	}{
		{"1h", "5m", "2026-03-10 09:04", "2026-03-10 09:05"},
		{"1h", "5m", "2026-03-10 09:05", "2026-03-10 10:05"}, // strictly after
		{"1h", "", "2026-03-10 23:50", "2026-03-11 00:00"},   // rollover
		{"1h", "5m", "2026-03-10 23:50", "2026-03-11 00:05"},
		{"7h", "", "2026-03-10 20:59", "2026-03-10 21:00"}, // 0, 7, 14, 21
		{"7h", "", "2026-03-10 21:00", "2026-03-11 00:00"}, // resets at midnight: not 04:00
		{"24h", "30m", "2026-03-10 00:30", "2026-03-11 00:30"},
		{"15m", "", "2026-03-10 00:00", "2026-03-10 00:15"},
		{"15m", "", "2026-03-09 23:59", "2026-03-10 00:00"},
	} {
		r := wire.Routine{Every: tc.every, Offset: tc.offset}
		got, err := Next(r, at(t, time.UTC, tc.after), time.UTC)
		if err != nil || !got.Equal(at(t, time.UTC, tc.want)) {
			t.Errorf("every %s+%s after %s: %v %v, want %s", tc.every, tc.offset, tc.after, got, err, tc.want)
		}
		if got.Location() != time.UTC {
			t.Errorf("not UTC: %v", got)
		}
	}
	if _, err := Next(wire.Routine{Every: "x"}, time.Now(), time.UTC); err == nil {
		t.Error("bad every accepted")
	}
	if _, err := Next(wire.Routine{}, time.Now(), time.UTC); err == nil {
		t.Error("no cadence accepted")
	}
}

func TestNextAt(t *testing.T) {
	r := wire.Routine{At: []string{"07:30", "18:00"}}
	for _, tc := range []struct{ after, want string }{
		{"2026-03-10 06:00", "2026-03-10 07:30"},
		{"2026-03-10 07:30", "2026-03-10 18:00"},
		{"2026-03-10 18:00", "2026-03-11 07:30"},
		{"2026-03-10 23:50", "2026-03-11 07:30"},
	} {
		got, err := Next(r, at(t, time.UTC, tc.after), time.UTC)
		if err != nil || !got.Equal(at(t, time.UTC, tc.want)) {
			t.Errorf("after %s: %v %v, want %s", tc.after, got, err, tc.want)
		}
	}
	// Unsorted works too (Validate sorts, Next does not rely on it).
	got, _ := Next(wire.Routine{At: []string{"18:00", "07:30"}}, at(t, time.UTC, "2026-03-10 06:00"), time.UTC)
	if !got.Equal(at(t, time.UTC, "2026-03-10 07:30")) {
		t.Errorf("unsorted: %v", got)
	}
	if _, err := Next(wire.Routine{At: []string{"7:30"}}, time.Now(), time.UTC); err == nil {
		t.Error("bad at accepted")
	}
}

func TestNextBerlin(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("no Europe/Berlin:", err)
	}
	// 2026-03-29: clocks go from 02:00 CET to 03:00 CEST (a 23-hour day).
	for _, tc := range []struct {
		r           wire.Routine
		after, want string
	}{
		// Every anchors at the local midnight: 1h from 00:00 CET lands on
		// 01:00, 03:00 (02:00 does not exist), 04:00 …
		{wire.Routine{Every: "1h"}, "2026-03-29 01:30", "2026-03-29 03:00"},
		{wire.Routine{Every: "1h"}, "2026-03-29 03:00", "2026-03-29 04:00"},
		// 7h on the short day: 00:00, 08:00 (7h later by the clock is 08:00
		// CEST), 15:00, 22:00, then the next midnight (not 05:00).
		{wire.Routine{Every: "7h"}, "2026-03-29 00:00", "2026-03-29 08:00"},
		{wire.Routine{Every: "7h"}, "2026-03-29 22:00", "2026-03-30 00:00"},
		{wire.Routine{Every: "1h", Offset: "5m"}, "2026-03-29 09:04", "2026-03-29 09:05"},
		{wire.Routine{Every: "1h", Offset: "5m"}, "2026-03-29 09:05", "2026-03-29 10:05"},
		// At follows the wall clock across the change.
		{wire.Routine{At: []string{"07:30", "18:00"}}, "2026-03-28 18:00", "2026-03-29 07:30"},
		{wire.Routine{At: []string{"07:30", "18:00"}}, "2026-03-29 23:50", "2026-03-30 07:30"},
		{wire.Routine{Every: "1h"}, "2026-03-29 23:50", "2026-03-30 00:00"},
		// 2026-10-25: 03:00 CEST goes back to 02:00 CET (a 25-hour day);
		// hourly runs 25 times and the midnight anchor holds. "24h" is
		// midnight + 24h: 23:00 that day, a second run (daily is At).
		{wire.Routine{Every: "1h"}, "2026-10-25 23:30", "2026-10-26 00:00"},
		{wire.Routine{Every: "24h"}, "2026-10-25 00:00", "2026-10-25 23:00"},
		{wire.Routine{Every: "24h"}, "2026-10-25 23:00", "2026-10-26 00:00"},
	} {
		got, err := Next(tc.r, at(t, loc, tc.after), loc)
		want := at(t, loc, tc.want)
		if err != nil || !got.Equal(want) {
			t.Errorf("%+v after %s: %v %v, want %s (%v)", tc.r, tc.after, got.In(loc), err, tc.want, want)
		}
	}
	// The 25-hour day has 25 hourly runs, the 23-hour day 23.
	for day, want := range map[string]int{"2026-10-25": 25, "2026-03-29": 23, "2026-03-30": 24} {
		n := 0
		for cur := at(t, loc, day+" 00:00").Add(-time.Second); ; n++ {
			next, _ := Next(wire.Routine{Every: "1h"}, cur, loc)
			if next.In(loc).Format("2006-01-02") != day {
				break
			}
			cur = next
		}
		if n != want {
			t.Errorf("%s: %d hourly runs, want %d", day, n, want)
		}
	}
}

func TestLate(t *testing.T) {
	due := at(t, time.UTC, "2026-03-10 09:00")
	hourly := wire.Routine{Every: "1h"}
	if Late(hourly, due, due.Add(59*time.Minute)) || !Late(hourly, due, due.Add(61*time.Minute)) {
		t.Error("every")
	}
	daily := wire.Routine{At: []string{"09:00"}}
	if Late(daily, due, due.Add(LateAt)) || !Late(daily, due, due.Add(LateAt+time.Second)) {
		t.Error("at")
	}
}
