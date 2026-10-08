package host

import (
	"context"
	"math"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/pkg/session"
)

// MachineMonitor caches machine stats (memory, battery, lid) for snapshots.
// The first call samples synchronously; afterwards a stale value is returned
// while a background refresh runs, so snapshots never wait on it.
type MachineMonitor struct {
	TTL    time.Duration                              // default 10s
	Sample func(context.Context) session.MachineStats // test seam; nil samples this machine

	mu         sync.Mutex
	value      session.MachineStats
	at         time.Time
	refreshing bool
}

// Stats returns the cached stats with Agents counted from the snapshot.
func (m *MachineMonitor) Stats(ctx context.Context, terminals []session.Terminal) *session.MachineStats {
	m.mu.Lock()
	if m.TTL <= 0 {
		m.TTL = 10 * time.Second
	}
	if m.Sample == nil {
		m.Sample = SampleMachine
	}
	if m.at.IsZero() {
		m.mu.Unlock()
		sampleCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		value := m.Sample(sampleCtx)
		cancel()
		m.mu.Lock()
		m.value, m.at = value, time.Now()
	} else if time.Since(m.at) > m.TTL && !m.refreshing {
		m.refreshing = true
		go func() {
			sampleCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			value := m.Sample(sampleCtx)
			cancel()
			m.mu.Lock()
			m.value, m.at, m.refreshing = value, time.Now(), false
			m.mu.Unlock()
		}()
	}
	stats := m.value
	m.mu.Unlock()
	stats.Agents = 0
	for _, t := range terminals {
		if session.AgentRoles[t.Role] && !t.Exited {
			stats.Agents++
		}
	}
	return &stats
}

// SampleMachine reads memory, battery and lid state. macOS uses
// memory_pressure, pmset and ioreg; Linux reads /proc/meminfo only.
func SampleMachine(ctx context.Context) session.MachineStats {
	var s session.MachineStats
	if runtime.GOOS == "linux" {
		if data, err := os.ReadFile("/proc/meminfo"); err == nil {
			s.MemoryUsed = parseMeminfo(string(data))
		}
		return s
	}
	if runtime.GOOS != "darwin" {
		return s
	}
	if out, err := exec.CommandContext(ctx, "memory_pressure", "-Q").Output(); err == nil {
		s.MemoryUsed = parseMemoryPressure(string(out))
	}
	if out, err := exec.CommandContext(ctx, "pmset", "-g", "batt").Output(); err == nil {
		s.Battery, s.OnBattery = parsePower(string(out))
	}
	if out, err := exec.CommandContext(ctx, "ioreg", "-r", "-k", "AppleClamshellState", "-d", "1").Output(); err == nil {
		s.LidClosed = parseClamshell(string(out))
	}
	return s
}

var memoryFree = regexp.MustCompile(`free percentage:\s*(\d+)%`)
var clamshell = regexp.MustCompile(`"AppleClamshellState"\s*=\s*(Yes|No)`)

func fraction(v float64) *float64 {
	v = math.Round(min(max(v, 0), 1)*100) / 100
	return &v
}

func parseMemoryPressure(out string) *float64 {
	m := memoryFree.FindStringSubmatch(out)
	if m == nil {
		return nil
	}
	free, _ := strconv.Atoi(m[1])
	return fraction(1 - float64(free)/100)
}

func parseMeminfo(out string) *float64 {
	fields := map[string]float64{}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			v, _ := strconv.ParseFloat(parts[1], 64)
			fields[strings.TrimSuffix(parts[0], ":")] = v
		}
	}
	if fields["MemTotal"] <= 0 || fields["MemAvailable"] <= 0 {
		return nil
	}
	return fraction(1 - fields["MemAvailable"]/fields["MemTotal"])
}

// parsePower reads pmset output. A Mac without a battery reports only the
// power source, so battery stays unknown and onBattery false.
func parsePower(out string) (*float64, *bool) {
	onBattery := strings.Contains(out, "'Battery Power'")
	if !onBattery && !strings.Contains(out, "'AC Power'") && !strings.Contains(out, "'UPS Power'") {
		return nil, nil
	}
	if percent, _, ok := parseBattery(out); ok {
		return fraction(float64(percent) / 100), &onBattery
	}
	return nil, &onBattery
}

func parseClamshell(out string) *bool {
	m := clamshell.FindStringSubmatch(out)
	if m == nil {
		return nil
	}
	closed := m[1] == "Yes"
	return &closed
}
