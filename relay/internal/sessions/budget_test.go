package sessions

import (
	"os"
	"time"
)

// budgetSlack: the race detector slows the pure-Go SQLite tenfold or
// more (budgets are only logged there); CI machines get 5×.
func budgetSlack() time.Duration {
	switch {
	case raceEnabled:
		return 1000
	case os.Getenv("CI") != "":
		return 5
	}
	return 1
}

func rounds(n int) int {
	if raceEnabled {
		return n / 20
	}
	return n
}
