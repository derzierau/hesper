package transport_test

import (
	"os"
	"testing"

	"github.com/derzierau/hesper/relay/internal/fakeagent"
)

// The test binary is also the fake agent programs (never a real claude or
// codex) that the hesperd instances of remote_test.go start.
func TestMain(m *testing.M) {
	if os.Getenv("AGENTS_FAKE") == "1" && len(os.Args) > 1 {
		fakeagent.Run(os.Args[1:])
		os.Exit(0)
	}
	os.Exit(m.Run())
}
