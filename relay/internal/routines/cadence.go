package routines

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// The cadence: when a routine runs. Every anchors at local midnight
// (midnight + Offset + k·Every, starting over each day); At is a list of
// local wall-clock times. Days are taken from time.Date in the store's
// location, so a DST day is 23 or 25 hours long and the cadence follows
// the wall clock.

var atTime = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// Validate normalises r in place and fills its defaults (RunPolicy, Name,
// Offset); a bad routine is a *wire.Error with wire.CodeInvalid. An empty
// ID passes: the store assigns one. Durations keep the text they were
// given ("1h" stays "1h").
func Validate(r *wire.Routine) error {
	if r.ID != "" && !ID.MatchString(r.ID) {
		return wire.Errorf(wire.CodeInvalid, "routine id %q: want r- and 1-40 of [a-z0-9-]", r.ID)
	}
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" {
		r.Name = strings.TrimPrefix(r.ID, "r-")
	}
	if r.Name == "" {
		return wire.Errorf(wire.CodeInvalid, "a routine needs a name")
	}
	if err := validateCadence(r); err != nil {
		return err
	}
	if err := validateSpawn(r); err != nil {
		return err
	}
	return validatePolicy(&r.Policy)
}

func validateCadence(r *wire.Routine) error {
	r.Every, r.Offset = strings.TrimSpace(r.Every), strings.TrimSpace(r.Offset)
	switch {
	case r.Every == "" && len(r.At) == 0:
		return wire.Errorf(wire.CodeInvalid, "a routine needs every or at")
	case r.Every != "" && len(r.At) > 0:
		return wire.Errorf(wire.CodeInvalid, "every and at exclude each other")
	case r.Every != "":
		every, err := time.ParseDuration(r.Every)
		if err != nil {
			return wire.Errorf(wire.CodeInvalid, "every %q: want a duration like 15m or 1h", r.Every)
		}
		if every < MinEvery || every > MaxEvery {
			return wire.Errorf(wire.CodeInvalid, "every %s: want %s to %s", r.Every, MinEvery, MaxEvery)
		}
		if r.Offset == "" {
			r.Offset = "0s"
		}
		offset, err := time.ParseDuration(r.Offset)
		if err != nil {
			return wire.Errorf(wire.CodeInvalid, "offset %q: want a duration like 5m", r.Offset)
		}
		if offset < 0 || offset >= every {
			return wire.Errorf(wire.CodeInvalid, "offset %s: want 0 up to every (%s)", r.Offset, r.Every)
		}
		return nil
	}
	if r.Offset != "" {
		return wire.Errorf(wire.CodeInvalid, "offset goes with every")
	}
	seen := map[string]bool{}
	var at []string
	for _, t := range r.At {
		t = strings.TrimSpace(t)
		if !atTime.MatchString(t) {
			return wire.Errorf(wire.CodeInvalid, "at %q: want HH:MM (24h)", t)
		}
		if !seen[t] {
			seen[t] = true
			at = append(at, t)
		}
	}
	sort.Strings(at)
	r.At = at
	return nil
}

func validateSpawn(r *wire.Routine) error {
	sp := &r.Spawn
	if strings.TrimSpace(sp.Task) == "" {
		return wire.Errorf(wire.CodeInvalid, "a routine needs a task")
	}
	if len(sp.Task) > TaskLimit {
		return wire.Errorf(wire.CodeInvalid, "the task is over %d KiB", TaskLimit>>10)
	}
	if sp.Project == "" || !filepath.IsAbs(sp.Project) {
		return wire.Errorf(wire.CodeInvalid, "project %q: want an absolute path", sp.Project)
	}
	if sp.Machine != "" && sp.Machine != r.Machine {
		return wire.Errorf(wire.CodeInvalid, "the spawn's machine %q is not the routine's (%q)", sp.Machine, r.Machine)
	}
	switch sp.Kind {
	case "", wire.KindClaude, wire.KindCodex, wire.KindShell:
	default:
		return wire.Errorf(wire.CodeInvalid, "kind %q: want claude, codex or shell", sp.Kind)
	}
	// The daemon's: it names the run and places it.
	sp.Name, sp.Caller, sp.Parent, sp.Depth, sp.Bring, sp.Draft = "", "", "", 0, nil, ""
	return nil
}

func validatePolicy(p *wire.RunPolicy) error {
	p.Timeout = strings.TrimSpace(p.Timeout)
	if p.Timeout == "" {
		p.Timeout = "1h"
	}
	timeout, err := time.ParseDuration(p.Timeout)
	if err != nil {
		return wire.Errorf(wire.CodeInvalid, "timeout %q: want a duration like 30m", p.Timeout)
	}
	if timeout < MinTimeout || timeout > MaxTimeout {
		return wire.Errorf(wire.CodeInvalid, "timeout %s: want %s to %s", p.Timeout, MinTimeout, MaxTimeout)
	}
	switch p.Overlap {
	case "":
		p.Overlap = wire.OverlapSkip
	case wire.OverlapSkip, wire.OverlapReplace:
	default:
		return wire.Errorf(wire.CodeInvalid, "overlap %q: want skip or replace", p.Overlap)
	}
	switch p.CatchUp {
	case "":
		p.CatchUp = wire.CatchUpOnce
	case wire.CatchUpOnce, wire.CatchUpNone:
	default:
		return wire.Errorf(wire.CodeInvalid, "catchUp %q: want once or none", p.CatchUp)
	}
	switch {
	case p.KeepRuns == 0:
		p.KeepRuns = DefaultKeepRuns
	case p.KeepRuns < 0 || p.KeepRuns > MaxKeepRuns:
		return wire.Errorf(wire.CodeInvalid, "keepRuns %d: want 1 to %d", p.KeepRuns, MaxKeepRuns)
	}
	return nil
}

// Next is the first instant strictly after after that r runs at, in UTC.
// Every: local midnight of after's day + Offset + k·Every while that is
// the same local day, else the next day's first; At: today's earliest
// time after after, else tomorrow's earliest.
func Next(r wire.Routine, after time.Time, loc *time.Location) (time.Time, error) {
	if loc == nil {
		loc = time.Local
	}
	local := after.In(loc)
	y, m, d := local.Date()
	midnight := time.Date(y, m, d, 0, 0, 0, 0, loc)
	tomorrow := time.Date(y, m, d+1, 0, 0, 0, 0, loc)
	if r.Every != "" {
		every, err := time.ParseDuration(r.Every)
		if err != nil {
			return time.Time{}, wire.Errorf(wire.CodeInvalid, "every %q: %v", r.Every, err)
		}
		if every <= 0 {
			return time.Time{}, wire.Errorf(wire.CodeInvalid, "every %q: want a positive duration", r.Every)
		}
		var offset time.Duration
		if r.Offset != "" {
			if offset, err = time.ParseDuration(r.Offset); err != nil {
				return time.Time{}, wire.Errorf(wire.CodeInvalid, "offset %q: %v", r.Offset, err)
			}
		}
		first := midnight.Add(offset)
		next := first
		if !after.Before(first) {
			next = first.Add((after.Sub(first)/every + 1) * every)
		}
		if !next.Before(tomorrow) {
			next = tomorrow.Add(offset)
		}
		return next.UTC(), nil
	}
	if len(r.At) == 0 {
		return time.Time{}, wire.Errorf(wire.CodeInvalid, "a routine needs every or at")
	}
	var today, nextDay time.Time
	for _, at := range r.At {
		hh, mm, ok := parseAt(at)
		if !ok {
			return time.Time{}, wire.Errorf(wire.CodeInvalid, "at %q: want HH:MM", at)
		}
		if t := time.Date(y, m, d, hh, mm, 0, 0, loc); t.After(after) && (today.IsZero() || t.Before(today)) {
			today = t
		}
		if t := time.Date(y, m, d+1, hh, mm, 0, 0, loc); nextDay.IsZero() || t.Before(nextDay) {
			nextDay = t
		}
	}
	if !today.IsZero() {
		return today.UTC(), nil
	}
	return nextDay.UTC(), nil
}

func parseAt(s string) (hh, mm int, ok bool) {
	if !atTime.MatchString(s) {
		return 0, 0, false
	}
	hh = int(s[0]-'0')*10 + int(s[1]-'0')
	mm = int(s[3]-'0')*10 + int(s[4]-'0')
	return hh, mm, true
}

// Late reports whether now is too late for the run due at due under
// CatchUp "none": more than one Every (At: more than LateAt) after it.
func Late(r wire.Routine, due, now time.Time) bool {
	limit := LateAt
	if r.Every != "" {
		every, err := time.ParseDuration(r.Every)
		if err != nil {
			return false
		}
		limit = every
	}
	return now.Sub(due) > limit
}
