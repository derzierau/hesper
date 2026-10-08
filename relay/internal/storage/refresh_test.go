package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/internal/auth"
	"github.com/derzierau/hesper/relay/internal/identity"
	"github.com/derzierau/hesper/relay/pkg/protocol"
	bolt "go.etcd.io/bbolt"
)

func enrolled(t *testing.T) (*Bolt, protocol.Credentials) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	verifier := identity.Secret()
	e := auth.Enrollment{ID: identity.Secret(), Name: "Mac", Role: identity.Host, Challenge: auth.Challenge(verifier), Expires: time.Now().Add(time.Minute)}
	if err = s.CreateEnrollment(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err = s.Approve(ctx, e.ID, auth.Principal{ID: "github:1", Login: "x"}); err != nil {
		t.Fatal(err)
	}
	_, c, err := s.Claim(ctx, e.ID, verifier, time.Minute, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return s, c
}

func (s *Bolt) editRefresh(t *testing.T, token string, edit func(*refreshRecord)) {
	t.Helper()
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(refreshes)
		var r refreshRecord
		if err := json.Unmarshal(b.Get([]byte(identity.Hash(token))), &r); err != nil {
			return err
		}
		edit(&r)
		return b.Put([]byte(identity.Hash(token)), protocol.JSON(r))
	})
	if err != nil {
		t.Fatal(err)
	}
}

func isReplay(err error) bool {
	var replay *auth.ReplayError
	return errors.As(err, &replay)
}

func TestRotateGraceReturnsTheSameSuccessor(t *testing.T) {
	ctx := context.Background()
	s, c := enrolled(t)
	_, first, err := s.Rotate(ctx, c.RefreshToken, c.Token, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// The response was lost: the device presents the same pair again.
	for i := 0; i < 2; i++ {
		_, again, err := s.Rotate(ctx, c.RefreshToken, c.Token, time.Minute)
		if err != nil || again.Token != first.Token || again.RefreshToken != first.RefreshToken || !again.ExpiresAt.Equal(first.ExpiresAt) || !again.RefreshExpiresAt.Equal(first.RefreshExpiresAt) {
			t.Fatal("grace retry did not return the same successor", err)
		}
	}
	if _, err = s.Authenticate(ctx, first.Token); err != nil {
		t.Fatal("device revoked by a grace retry:", err)
	}
	// The successor rotates normally, and then the original is plain reuse.
	if _, _, err = s.Rotate(ctx, first.RefreshToken, first.Token, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Rotate(ctx, c.RefreshToken, c.Token, time.Minute); !isReplay(err) {
		t.Fatal("reuse after the successor was used not detected:", err)
	}
	if _, err = s.Authenticate(ctx, first.Token); err == nil {
		t.Fatal("reuse did not revoke the device")
	}
}

func TestRotateReuseOutsideTheGraceIsDetected(t *testing.T) {
	ctx := context.Background()
	for name, retry := range map[string]func(*Bolt, protocol.Credentials) error{
		"no proof": func(s *Bolt, c protocol.Credentials) error {
			_, _, err := s.Rotate(ctx, c.RefreshToken, "", time.Minute)
			return err
		},
		"wrong proof": func(s *Bolt, c protocol.Credentials) error {
			_, _, err := s.Rotate(ctx, c.RefreshToken, identity.Secret(), time.Minute)
			return err
		},
		"successor's access token as proof": func(s *Bolt, c protocol.Credentials) error {
			var r refreshRecord
			s.db.View(func(tx *bolt.Tx) error {
				return json.Unmarshal(tx.Bucket(refreshes).Get([]byte(identity.Hash(c.RefreshToken))), &r)
			})
			var next protocol.Credentials
			if err := openSuccessor(c.RefreshToken, r.Sealed, &next); err != nil {
				return err
			}
			_, _, err := s.Rotate(ctx, c.RefreshToken, next.Token, time.Minute)
			return err
		},
		"after the window": func(s *Bolt, c protocol.Credentials) error {
			s.editRefresh(t, c.RefreshToken, func(r *refreshRecord) { r.UsedAt = time.Now().Add(-auth.RefreshGrace - time.Second) })
			_, _, err := s.Rotate(ctx, c.RefreshToken, c.Token, time.Minute)
			return err
		},
		"after pruning": func(s *Bolt, c protocol.Credentials) error {
			s.editRefresh(t, c.RefreshToken, func(r *refreshRecord) { r.UsedAt = time.Now().Add(-auth.RefreshGrace - time.Second) })
			if err := s.PruneAuth(ctx); err != nil {
				return err
			}
			s.editRefresh(t, c.RefreshToken, func(r *refreshRecord) {
				if len(r.Sealed) != 0 {
					t.Error("prune kept the sealed successor")
				}
				r.UsedAt = time.Now()
			})
			_, _, err := s.Rotate(ctx, c.RefreshToken, c.Token, time.Minute)
			return err
		},
		"tampered sealed successor": func(s *Bolt, c protocol.Credentials) error {
			s.editRefresh(t, c.RefreshToken, func(r *refreshRecord) { r.Sealed[len(r.Sealed)-1] ^= 1 })
			_, _, err := s.Rotate(ctx, c.RefreshToken, c.Token, time.Minute)
			return err
		},
		"credential from before the grace (no proof hash)": func(s *Bolt, c protocol.Credentials) error {
			s.editRefresh(t, c.RefreshToken, func(r *refreshRecord) { r.Access = "" })
			_, _, err := s.Rotate(ctx, c.RefreshToken, c.Token, time.Minute)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, c := enrolled(t)
			_, next, err := s.Rotate(ctx, c.RefreshToken, c.Token, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if err = retry(s, c); !isReplay(err) {
				t.Fatal("reuse not detected:", err)
			}
			if _, err = s.Authenticate(ctx, next.Token); err == nil {
				t.Fatal("reuse did not revoke the device")
			}
		})
	}
}

func TestSealedSuccessorNeedsTheConsumedToken(t *testing.T) {
	s, c := enrolled(t)
	_, next, err := s.Rotate(context.Background(), c.RefreshToken, c.Token, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	s.db.View(func(tx *bolt.Tx) error {
		raw = append(raw, tx.Bucket(refreshes).Get([]byte(identity.Hash(c.RefreshToken)))...)
		return nil
	})
	if bytes.Contains(raw, []byte(next.RefreshToken)) || bytes.Contains(raw, []byte(next.Token)) {
		t.Fatal("successor stored in clear")
	}
	var r refreshRecord
	if err = json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	var opened protocol.Credentials
	if openSuccessor(identity.Secret(), r.Sealed, &opened) == nil {
		t.Fatal("sealed successor opened without the consumed refresh credential")
	}
}
