package storage

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/internal/identity"
	bolt "go.etcd.io/bbolt"
)

func TestEnrollmentIsSingleUseDurableAndRevocable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "relay.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	invitation, err := s.Invite(ctx, identity.Invitation{Owner: "a", Role: identity.Host, Expires: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	var wins atomic.Int32
	var device identity.Device
	var credential string
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, token, err := s.Redeem(ctx, invitation, "mac")
			if err == nil {
				wins.Add(1)
				device, credential = d, token
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("expected one redemption, got %d", wins.Load())
	}
	if _, err := s.Authenticate(ctx, invitation); err == nil {
		t.Fatal("invitation used as a device credential")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.Authenticate(ctx, credential)
	if err != nil || got.ID != device.ID {
		t.Fatalf("credential did not survive restart: %v", err)
	}
	if err := s.Revoke(ctx, device.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, credential); err == nil {
		t.Fatal("revoked credential accepted")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("database permissions: %v", info.Mode())
	}
}
func TestExpiredInvitation(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.Invite(context.Background(), identity.Invitation{Owner: "a", Role: identity.Host, Expires: time.Now().Add(-time.Second)})
	if err == nil {
		t.Fatal("accepted expired invitation")
	}
}

// A relay that still had phone push left a push-v1 bucket; Open drops it.
func TestOpenDropsLegacyPushTokens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.db")
	db, err := bolt.Open(path, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucket([]byte("push-v1"))
		if err != nil {
			return err
		}
		return b.Put([]byte("device"), []byte(`{"token":"ab"}`))
	}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.db.View(func(tx *bolt.Tx) error {
		if tx.Bucket([]byte("push-v1")) != nil {
			t.Error("push-v1 bucket kept")
		}
		return nil
	})
}
