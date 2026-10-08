package host

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/derzierau/hesper/relay/pkg/session"
)

// KeepAwake holds an idle-sleep assertion while any agent is working or waiting
// for an answer, so agents that outlive their windows stay reachable. It never
// prevents lid-close sleep and releases the assertion on low battery.
type KeepAwake struct {
	Snapshot   func(context.Context) (session.Snapshot, error)
	Interval   time.Duration
	MinBattery int // percent; below this on battery power the Mac may sleep
	Logger     *slog.Logger

	// Test seams; nil uses caffeinate(8) and pmset(1).
	Hold    func() (release func(), err error)
	Battery func(context.Context) (percent int, onBattery bool, ok bool)
}

// NeedsAwake reports whether a snapshot has an agent that is busy or waiting.
func NeedsAwake(s session.Snapshot) bool {
	for _, t := range s.Terminals {
		if t.Exited {
			continue
		}
		if t.State == "working" || t.Attention == "approval" || t.Attention == "question" {
			return true
		}
	}
	return false
}

func (k *KeepAwake) Run(ctx context.Context) {
	if k.Interval <= 0 {
		k.Interval = 30 * time.Second
	}
	if k.Logger == nil {
		k.Logger = slog.Default()
	}
	if k.Hold == nil {
		k.Hold = caffeinate
	}
	if k.Battery == nil {
		k.Battery = pmsetBattery
	}
	var release func()
	defer func() {
		if release != nil {
			release()
		}
	}()
	tick := time.NewTicker(k.Interval)
	defer tick.Stop()
	for {
		want, reason := k.wanted(ctx)
		switch {
		case want && release == nil:
			r, err := k.Hold()
			if err != nil {
				k.Logger.Warn("cannot hold sleep assertion", "error", err)
				break
			}
			release = r
			k.Logger.Info("holding idle-sleep assertion", "reason", reason)
		case !want && release != nil:
			release()
			release = nil
			k.Logger.Info("released idle-sleep assertion", "reason", reason)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (k *KeepAwake) wanted(ctx context.Context) (bool, string) {
	snapshot, err := k.Snapshot(ctx)
	if err != nil || !NeedsAwake(snapshot) {
		return false, "no agent working or waiting"
	}
	if percent, onBattery, ok := k.Battery(ctx); ok && onBattery && percent < k.MinBattery {
		return false, "battery low"
	}
	return true, "agent working or waiting"
}

func caffeinate() (func(), error) {
	// -w ties the assertion to this process, so a crashed host never leaves it.
	cmd := exec.Command("caffeinate", "-i", "-w", strconv.Itoa(os.Getpid()))
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }, nil
}

var batteryPercent = regexp.MustCompile(`(\d+)%`)

func pmsetBattery(ctx context.Context) (int, bool, bool) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "pmset", "-g", "batt").Output()
	if err != nil {
		return 0, false, false
	}
	return parseBattery(string(out))
}

func parseBattery(out string) (int, bool, bool) {
	m := batteryPercent.FindStringSubmatch(out)
	if m == nil {
		return 0, false, false // desktop Mac without a battery
	}
	percent, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false, false
	}
	return percent, strings.Contains(out, "'Battery Power'"), true
}
