package devicekey

import (
	"encoding/base64"
	"testing"
)

// The golden vectors of docs/iphone/onboarding.md (Interfaces); the iPhone
// app's tests use the same.
func TestOfferGoldenVectors(t *testing.T) {
	secret := make([]byte, OfferSecretSize)
	for i := range secret {
		secret[i] = byte(i)
	}
	if got := OfferProof(secret, "MACHINE_ID", "iPhone", "KEY_DER_B64", "STRONG_DER_B64", "E2E_B64"); got != "egD7PtFnM87JDQYgwg/Ufl0e0cOSazrK/X4p5ukb1Wc=" {
		t.Fatalf("proof %s", got)
	}
	const want = "HESPER1:eyJ2IjoxLCJyZWxheSI6Imh0dHBzOi8vcmVsYXkuZXhhbXBsZS5jb20iLCJtYWNoaW5lIjoiTUFDSElORV9JRCIsIm5hbWUiOiJzdHVkaW8iLCJob3N0S2V5IjoiQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQT0iLCJyaWdodHMiOlsib2JzZXJ2ZSIsImFuc3dlciIsInR5cGUiLCJ0cmFuc2ZlciJdLCJvZmZlciI6IkFBRUNBd1FGQmdjSUNRb0xEQTBPRHciLCJleHBpcmVzIjoxNzkzNjUwOTU2fQ"
	code := PairingCode{V: 1, Relay: "https://relay.example.com", Machine: "MACHINE_ID", Name: "studio",
		HostKey: base64.StdEncoding.EncodeToString(make([]byte, 32)), Rights: []string{Observe, Answer, Type, Transfer},
		Offer: base64.RawURLEncoding.EncodeToString(secret), Expires: 1793650956}
	if got := code.Text(); got != want {
		t.Fatalf("code %s", got)
	}
	parsed, err := ParsePairingCode(want)
	if err != nil || parsed.Name != "studio" || parsed.Expires != 1793650956 || len(parsed.Rights) != 4 {
		t.Fatalf("parsed %+v %v", parsed, err)
	}
	if s, err := parsed.Secret(); err != nil || string(s) != string(secret) {
		t.Fatalf("secret %x %v", s, err)
	}
	for _, bad := range []string{"", "HESPER1:", "HESPER2:" + want[8:], "https://relay.example.com", "HESPER1:e30",
		PairingCode{V: 2, Relay: "r", Machine: "m", Name: "n", HostKey: code.HostKey, Rights: code.Rights, Offer: code.Offer, Expires: 1}.Text(),
		PairingCode{V: 1, Relay: "r", Machine: "m", Name: "n", HostKey: code.HostKey, Rights: code.Rights, Offer: "AAEC", Expires: 1}.Text()} {
		if _, err := ParsePairingCode(bad); err != ErrNotPairingCode {
			t.Errorf("%q: %v", bad, err)
		}
	}
}
