package storage

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"time"

	"github.com/derzierau/hesper/relay/internal/auth"
	"github.com/derzierau/hesper/relay/internal/identity"
	"github.com/derzierau/hesper/relay/pkg/protocol"
	bolt "go.etcd.io/bbolt"
)

var enrollments = []byte("enrollments-v1")
var flows = []byte("oauth-flows-v1")
var principals = []byte("principals-v1")
var refreshes = []byte("refresh-v1")
var _ auth.Repository = (*Bolt)(nil)

type accessRecord struct {
	DeviceID string    `json:"deviceId"`
	Expires  time.Time `json:"expires"`
}
type refreshRecord struct {
	DeviceID string    `json:"deviceId"`
	Expires  time.Time `json:"expires"`
	Used     bool      `json:"used"`
	// Access is the hash of the access token issued with this refresh
	// credential: the device's proof for the lost-response grace.
	Access string    `json:"access,omitempty"`
	UsedAt time.Time `json:"usedAt,omitempty"`
	// Successor is the hash of the one refresh credential issued in exchange.
	Successor string `json:"successor,omitempty"`
	// Sealed holds the successor pair for auth.RefreshGrace, encrypted under a
	// key derived from this refresh credential. The relay stores that
	// credential only as a hash, so the database alone cannot open it.
	Sealed []byte `json:"sealed,omitempty"`
}

func (s *Bolt) CreateEnrollment(ctx context.Context, e auth.Enrollment) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(enrollments)
		if b.Stats().KeyN >= 4096 {
			return protocol.Err("busy", "Too many pending enrollments")
		}
		return b.Put([]byte(e.ID), protocol.JSON(e))
	})
}
func (s *Bolt) Enrollment(ctx context.Context, id string) (auth.Enrollment, error) {
	var e auth.Enrollment
	if err := ctx.Err(); err != nil {
		return e, err
	}
	err := s.db.View(func(tx *bolt.Tx) error {
		if json.Unmarshal(tx.Bucket(enrollments).Get([]byte(id)), &e) != nil || e.Claimed || !e.Expires.After(time.Now()) {
			return protocol.Err("expired", "Enrollment expired or was already claimed")
		}
		return nil
	})
	return e, err
}
func (s *Bolt) PutFlow(ctx context.Context, f auth.Flow) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(flows)
		if b.Stats().KeyN >= 4096 {
			return protocol.Err("busy", "Too many pending sign-ins")
		}
		return b.Put([]byte(identity.Hash(f.State)), protocol.JSON(f))
	})
}
func (s *Bolt) TakeFlow(ctx context.Context, state string) (auth.Flow, error) {
	var f auth.Flow
	if err := ctx.Err(); err != nil {
		return f, err
	}
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(flows)
		key := []byte(identity.Hash(state))
		if json.Unmarshal(b.Get(key), &f) != nil || !f.Expires.After(time.Now()) {
			return protocol.Err("expired", "Sign-in transaction expired or was already used")
		}
		return b.Delete(key)
	})
	return f, err
}
func (s *Bolt) Approve(ctx context.Context, id string, p auth.Principal) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(enrollments)
		var e auth.Enrollment
		if json.Unmarshal(b.Get([]byte(id)), &e) != nil || e.Claimed || e.Principal.ID != "" || !e.Expires.After(time.Now()) {
			return protocol.Err("expired", "Enrollment is no longer available")
		}
		e.Principal = p
		if err := tx.Bucket(principals).Put([]byte(p.ID), protocol.JSON(p)); err != nil {
			return err
		}
		return b.Put([]byte(id), protocol.JSON(e))
	})
}
func issue(tx *bolt.Tx, d identity.Device, accessTTL time.Duration, refreshExpiry time.Time) (protocol.Credentials, error) {
	c := protocol.Credentials{DeviceID: d.ID, Role: string(d.Role), Token: identity.Secret(), RefreshToken: identity.Secret(), ExpiresAt: minTime(time.Now().Add(accessTTL), refreshExpiry), RefreshExpiresAt: refreshExpiry}
	if err := tx.Bucket(tokens).Put([]byte(identity.Hash(c.Token)), protocol.JSON(accessRecord{DeviceID: d.ID, Expires: c.ExpiresAt})); err != nil {
		return c, err
	}
	err := tx.Bucket(refreshes).Put([]byte(identity.Hash(c.RefreshToken)), protocol.JSON(refreshRecord{DeviceID: d.ID, Expires: refreshExpiry, Access: identity.Hash(c.Token)}))
	return c, err
}
func (s *Bolt) Claim(ctx context.Context, id, verifier string, accessTTL, refreshTTL time.Duration) (identity.Device, protocol.Credentials, error) {
	var d identity.Device
	var credentials protocol.Credentials
	if err := ctx.Err(); err != nil {
		return d, credentials, err
	}
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(enrollments)
		var e auth.Enrollment
		if json.Unmarshal(b.Get([]byte(id)), &e) != nil || e.Claimed || !e.Expires.After(time.Now()) {
			return protocol.Err("expired", "Enrollment is unavailable")
		}
		if subtle.ConstantTimeCompare([]byte(e.Challenge), []byte(auth.Challenge(verifier))) != 1 {
			return protocol.Err("unauthorized", "Enrollment proof rejected")
		}
		if e.Principal.ID == "" {
			return protocol.Err("authorization_pending", "Waiting for browser approval")
		}
		d = identity.Device{ID: identity.Secret(), Owner: e.Principal.ID, Role: e.Role, Name: e.Name}
		if err := tx.Bucket(devices).Put([]byte(d.ID), protocol.JSON(d)); err != nil {
			return err
		}
		var err error
		credentials, err = issue(tx, d, accessTTL, time.Now().Add(refreshTTL))
		if err != nil {
			return err
		}
		e.Claimed = true
		return b.Put([]byte(id), protocol.JSON(e))
	})
	return d, credentials, err
}
func (s *Bolt) Rotate(ctx context.Context, token, proof string, accessTTL time.Duration) (identity.Device, protocol.Credentials, error) {
	var d identity.Device
	var c protocol.Credentials
	replay := false
	if err := ctx.Err(); err != nil {
		return d, c, err
	}
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(refreshes)
		key := []byte(identity.Hash(token))
		var r refreshRecord
		if json.Unmarshal(b.Get(key), &r) != nil || !r.Expires.After(time.Now()) {
			return protocol.Err("unauthorized", "Refresh credential expired or invalid; sign in again")
		}
		if json.Unmarshal(tx.Bucket(devices).Get([]byte(r.DeviceID)), &d) != nil || d.Revoked {
			return protocol.Err("unauthorized", "Device has been revoked")
		}
		if r.Used {
			if next, ok := graceSuccessor(tx, token, proof, r); ok {
				c = next
				return nil
			}
			d.Revoked = true
			replay = true
			return tx.Bucket(devices).Put([]byte(d.ID), protocol.JSON(d))
		}
		// Preserve the absolute family expiry: unattended machines must re-enroll
		// after the configured period rather than acquire perpetual authorization.
		var err error
		if c, err = issue(tx, d, accessTTL, r.Expires); err != nil {
			return err
		}
		r.Used = true
		r.UsedAt = time.Now()
		r.Successor = identity.Hash(c.RefreshToken)
		if r.Sealed, err = sealSuccessor(token, c); err != nil {
			return err
		}
		return b.Put(key, protocol.JSON(r))
	})
	if replay && err == nil {
		err = &auth.ReplayError{DeviceID: d.ID}
	}
	return d, c, err
}

// graceSuccessor returns the successor already issued for the consumed refresh
// credential token when the same device presents it again shortly after: within
// auth.RefreshGrace, with the access token issued alongside it as proof, and
// only while that single successor is still unused and unexpired. Everything
// else is reuse.
func graceSuccessor(tx *bolt.Tx, token, proof string, r refreshRecord) (protocol.Credentials, bool) {
	var c protocol.Credentials
	if proof == "" || r.Access == "" || r.Successor == "" || len(r.Sealed) == 0 {
		return c, false
	}
	if age := time.Since(r.UsedAt); age < 0 || age > auth.RefreshGrace {
		return c, false
	}
	if subtle.ConstantTimeCompare([]byte(identity.Hash(proof)), []byte(r.Access)) != 1 {
		return c, false
	}
	if openSuccessor(token, r.Sealed, &c) != nil || subtle.ConstantTimeCompare([]byte(identity.Hash(c.RefreshToken)), []byte(r.Successor)) != 1 {
		return protocol.Credentials{}, false
	}
	var next refreshRecord
	if json.Unmarshal(tx.Bucket(refreshes).Get([]byte(r.Successor)), &next) != nil || next.Used || next.DeviceID != r.DeviceID || !next.Expires.After(time.Now()) {
		return protocol.Credentials{}, false
	}
	return c, true
}

func successorAEAD(token string) (cipher.AEAD, error) {
	// wire name: kept as "ghosty refresh successor v1" until the next relay
	// deploy (stored successors are sealed with it).
	key := sha256.Sum256([]byte("ghosty refresh successor v1\x00" + token))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
func sealSuccessor(token string, c protocol.Credentials) ([]byte, error) {
	aead, err := successorAEAD(token)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, protocol.JSON(c), []byte(identity.Hash(token))), nil
}
func openSuccessor(token string, sealed []byte, c *protocol.Credentials) error {
	aead, err := successorAEAD(token)
	if err != nil {
		return err
	}
	if len(sealed) < aead.NonceSize() {
		return errors.New("sealed successor too short")
	}
	plain, err := aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], []byte(identity.Hash(token)))
	if err != nil {
		return err
	}
	return json.Unmarshal(plain, c)
}

func (s *Bolt) PruneAuth(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		// Successors sealed for the lost-response grace are useless after it.
		b := tx.Bucket(refreshes)
		stale := map[string]refreshRecord{}
		if err := b.ForEach(func(k, v []byte) error {
			var r refreshRecord
			if json.Unmarshal(v, &r) == nil && len(r.Sealed) > 0 && time.Since(r.UsedAt) > auth.RefreshGrace {
				r.Sealed = nil
				stale[string(k)] = r
			}
			return nil
		}); err != nil {
			return err
		}
		for k, r := range stale {
			if err := b.Put([]byte(k), protocol.JSON(r)); err != nil {
				return err
			}
		}
		for _, name := range [][]byte{enrollments, flows, refreshes, tokens} {
			cursor := tx.Bucket(name).Cursor()
			for k, v := cursor.First(); k != nil; k, v = cursor.Next() {
				var row struct {
					Expires time.Time `json:"expires"`
				}
				if json.Unmarshal(v, &row) == nil && !row.Expires.IsZero() && !row.Expires.After(time.Now()) {
					if err := cursor.Delete(); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
