package main

import (
	"os"
	"testing"

	"github.com/derzierau/hesper/relay/pkg/devicekey"
)

// Tests never use this Mac's Secure Enclave keys or the user's state
// directory: commands sign with software keys in a temporary directory.
func TestMain(m *testing.M) {
	// hesperctl mcp's tests run the test binary as hesperctl.
	if os.Getenv("HESPERCTL_TEST_MAIN") == "1" {
		main()
	}
	dir, err := os.MkdirTemp("", "hesperctl-keys-")
	if err != nil {
		panic(err)
	}
	testSigner := &devicekey.Software{Dir: dir}
	deviceSigner = func() devicekey.Signer { return testSigner }
	// Run inside a Hesper agent, the tests are still a person to their
	// own daemons (agent tree); tests that need an agent set these.
	os.Unsetenv("HESPER_AGENT_ID")
	os.Unsetenv("HESPER_MACHINE")
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
