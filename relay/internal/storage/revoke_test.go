package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/internal/identity"
	bolt "go.etcd.io/bbolt"
)

func TestRevokeDropsTheDevicesCredentials(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	redeem := func(name string) identity.Device {
		t.Helper()
		invitation, err := s.Invite(ctx, identity.Invitation{Owner: "a", Role: identity.Controller, Expires: time.Now().Add(time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		d, _, err := s.Redeem(ctx, invitation, name)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	gone, kept := redeem("gone"), redeem("kept")
	if err := s.Revoke(ctx, gone.ID); err != nil {
		t.Fatal(err)
	}
	count := func(id string) int {
		n := 0
		s.db.View(func(tx *bolt.Tx) error {
			for _, name := range [][]byte{tokens, refreshes} {
				tx.Bucket(name).ForEach(func(_, v []byte) error {
					if string(v) == id {
						n++
					}
					return nil
				})
			}
			return nil
		})
		return n
	}
	if n := count(gone.ID); n != 0 {
		t.Fatalf("%d credentials of the revoked device remain", n)
	}
	if n := count(kept.ID); n != 1 {
		t.Fatalf("the other device lost its credential (%d)", n)
	}
}
