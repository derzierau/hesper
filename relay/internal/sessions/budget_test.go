package sessions

import (
	"os"
	"testing"
)

// Latency budgets are wall-clock measurements: on a loaded machine, under
// the race detector (the pure-Go SQLite gets tenfold slower or more) or on
// a shared CI runner they say more about the machine than the code. They
// are always measured and logged, and fail the test only when asked for
// on a quiet machine: HESPER_BUDGETS=1 go test ./internal/sessions -run Budget
func budgetsEnforced() bool {
	return os.Getenv("HESPER_BUDGETS") == "1" && !raceEnabled
}

// overBudget reports a measurement over its budget: an error when budgets
// are enforced, a log line otherwise.
func overBudget(t *testing.T, format string, args ...any) {
	t.Helper()
	if budgetsEnforced() {
		t.Errorf(format, args...)
		return
	}
	t.Logf("over budget (not enforced; HESPER_BUDGETS=1 enforces it): "+format, args...)
}

func rounds(n int) int {
	if raceEnabled {
		return n / 20
	}
	return n
}
