package main

import (
	"os"
	"testing"

	"github.com/derzierau/hesper/relay/pkg/devicekey"
)

// Tests never use this Mac's Secure Enclave keys or the user's state
// directory: commands sign with software keys in a temporary directory.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "hesperctl-keys-")
	if err != nil {
		panic(err)
	}
	testSigner := &devicekey.Software{Dir: dir}
	deviceSigner = func() devicekey.Signer { return testSigner }
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
