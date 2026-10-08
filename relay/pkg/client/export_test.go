package client

import (
	"net/http"
	"testing"
)

// UseAuthTransport routes authentication requests through rt for one test.
func UseAuthTransport(t *testing.T, rt http.RoundTripper) {
	h := authHTTP()
	h.Transport = rt
	useAuthClient(t, h)
}
