package storage

import (
	"context"
	"github.com/derzierau/hesper/relay/internal/auth"
	"github.com/derzierau/hesper/relay/internal/identity"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestClaimAtomicProofAndExpiry(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	verifier := identity.Secret()
	e := auth.Enrollment{ID: identity.Secret(), Name: "Mac", Role: identity.Host, Challenge: auth.Challenge(verifier), Expires: time.Now().Add(time.Minute)}
	if err = s.CreateEnrollment(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err = s.Approve(ctx, e.ID, auth.Principal{ID: "github:12345678", Login: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Claim(ctx, e.ID, "bad-proof", time.Minute, time.Hour); err == nil {
		t.Fatal("bad proof accepted")
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, c, err := s.Claim(ctx, e.ID, verifier, time.Hour, time.Minute)
			if err == nil {
				wins.Add(1)
				if c.ExpiresAt.After(c.RefreshExpiresAt) {
					t.Error("access outlived refresh family")
				}
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatal("multiple claims succeeded", wins.Load())
	}
	f := auth.Flow{State: identity.Secret(), Expires: time.Now().Add(-time.Second)}
	s.PutFlow(ctx, f)
	if _, err = s.TakeFlow(ctx, f.State); err == nil {
		t.Fatal("expired OAuth state accepted")
	}
	if err = s.PruneAuth(ctx); err != nil {
		t.Fatal(err)
	}
}
