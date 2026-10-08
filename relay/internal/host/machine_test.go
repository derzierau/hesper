package host

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/session"
)

func TestMachineParsers(t *testing.T) {
	if v := parseMemoryPressure("The system has 34359738368 (2097152 pages with a page size of 16384).\nSystem-wide memory free percentage: 38%\n"); v == nil || *v != 0.62 {
		t.Fatalf("memory %v", v)
	}
	if parseMemoryPressure("nothing") != nil {
		t.Fatal("unknown memory reported")
	}
	if v := parseMeminfo("MemTotal:       16000000 kB\nMemFree:  100 kB\nMemAvailable:    4000000 kB\n"); v == nil || *v != 0.75 {
		t.Fatalf("meminfo %v", v)
	}
	battery, onBattery := parsePower("Now drawing from 'Battery Power'\n -InternalBattery-0 (id=1)\t18%; discharging; 1:02 remaining present: true\n")
	if battery == nil || *battery != 0.18 || onBattery == nil || !*onBattery {
		t.Fatal("laptop on battery misread")
	}
	battery, onBattery = parsePower("Now drawing from 'AC Power'\n")
	if battery != nil || onBattery == nil || *onBattery {
		t.Fatal("desktop Mac misread")
	}
	if battery, onBattery = parsePower(""); battery != nil || onBattery != nil {
		t.Fatal("unknown power reported")
	}
	if v := parseClamshell(`      "AppleClamshellCausesSleep" = No` + "\n" + `      "AppleClamshellState" = Yes`); v == nil || !*v {
		t.Fatal("closed lid misread")
	}
	if parseClamshell("") != nil {
		t.Fatal("lid reported on a Mac without one")
	}
}

func TestMachineMonitorCachesAndCountsAgents(t *testing.T) {
	var samples atomic.Int32
	half := 0.5
	m := &MachineMonitor{TTL: 50 * time.Millisecond, Sample: func(context.Context) session.MachineStats {
		samples.Add(1)
		return session.MachineStats{MemoryUsed: &half, Agents: 99}
	}}
	terminals := []session.Terminal{{Role: "claude"}, {Role: "codex"}, {Role: "terminal"}, {Role: "claude", Exited: true}}
	for range 5 {
		s := m.Stats(context.Background(), terminals)
		if s.Agents != 2 || s.MemoryUsed == nil || *s.MemoryUsed != 0.5 {
			t.Fatalf("stats %+v", s)
		}
	}
	if samples.Load() != 1 {
		t.Fatalf("sampled %d times within the TTL", samples.Load())
	}
	time.Sleep(60 * time.Millisecond)
	m.Stats(context.Background(), nil)
	deadline := time.Now().Add(time.Second)
	for samples.Load() != 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if samples.Load() != 2 {
		t.Fatal("stale stats never refreshed")
	}
}
