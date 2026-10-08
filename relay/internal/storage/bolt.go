package storage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/derzierau/hesper/relay/internal/identity"
	"github.com/derzierau/hesper/relay/pkg/protocol"
	bolt "go.etcd.io/bbolt"
)

type Bolt struct{ db *bolt.DB }

var _ identity.Repository = (*Bolt)(nil)
var invites = []byte("invitations-v1")
var devices = []byte("devices-v1")
var tokens = []byte("tokens-v1")

func Open(path string) (*Bolt, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, err
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{invites, devices, tokens, enrollments, flows, principals, refreshes} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return err
			}
		}
		// Drop phone push registrations left by relays that still had push.
		if tx.Bucket([]byte("push-v1")) != nil {
			return tx.DeleteBucket([]byte("push-v1"))
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Bolt{db}, nil
}
func (s *Bolt) Close() error { return s.db.Close() }
func (s *Bolt) Invite(ctx context.Context, invitation identity.Invitation) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !protocol.ValidID(invitation.Owner) || !invitation.Role.Valid() || !invitation.Expires.After(time.Now()) || invitation.Expires.After(time.Now().Add(time.Hour)) {
		return "", protocol.Err("invalid_request", "Invalid owner, role, or expiry (maximum one hour)")
	}
	token := identity.Secret()
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(invites)
		c := b.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var old identity.Invitation
			if json.Unmarshal(v, &old) != nil || !old.Expires.After(time.Now()) {
				if err := c.Delete(); err != nil {
					return err
				}
			}
		}
		return b.Put([]byte(identity.Hash(token)), protocol.JSON(invitation))
	})
	return token, err
}
func (s *Bolt) Redeem(ctx context.Context, invitation, name string) (identity.Device, string, error) {
	var device identity.Device
	if err := ctx.Err(); err != nil {
		return device, "", err
	}
	if !protocol.ValidID(invitation) || len(name) == 0 || len(name) > 80 {
		return device, "", protocol.Err("invalid_request", "Invalid invitation or name")
	}
	token := identity.Secret()
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(invites)
		key := []byte(identity.Hash(invitation))
		var inv identity.Invitation
		if json.Unmarshal(b.Get(key), &inv) != nil || !inv.Expires.After(time.Now()) {
			return protocol.Err("unauthorized", "Invitation is invalid, expired, or already used")
		}
		device = identity.Device{ID: identity.Secret(), Owner: inv.Owner, Role: inv.Role, Name: name}
		if err := tx.Bucket(devices).Put([]byte(device.ID), protocol.JSON(device)); err != nil {
			return err
		}
		if err := tx.Bucket(tokens).Put([]byte(identity.Hash(token)), []byte(device.ID)); err != nil {
			return err
		}
		return b.Delete(key)
	})
	return device, token, err
}
func (s *Bolt) Authenticate(ctx context.Context, token string) (identity.Device, error) {
	var device identity.Device
	if err := ctx.Err(); err != nil {
		return device, err
	}
	err := s.db.View(func(tx *bolt.Tx) error {
		id := tx.Bucket(tokens).Get([]byte(identity.Hash(token)))
		var access accessRecord
		if json.Unmarshal(id, &access) == nil {
			if !access.Expires.After(time.Now()) {
				return protocol.Err("unauthorized", "Access credential expired")
			}
			id = []byte(access.DeviceID)
		}
		if len(id) == 0 || json.Unmarshal(tx.Bucket(devices).Get(id), &device) != nil || device.Revoked {
			return protocol.Err("unauthorized", "Invalid device credential")
		}
		device.CredentialExpiresAt = access.Expires
		return nil
	})
	return device, err
}
func (s *Bolt) Devices(ctx context.Context, owner string) ([]identity.Device, error) {
	result := []identity.Device{}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(devices).ForEach(func(_, value []byte) error {
			var device identity.Device
			if err := json.Unmarshal(value, &device); err != nil {
				return err
			}
			if owner == "" || device.Owner == owner {
				result = append(result, device)
			}
			return nil
		})
	})
	return result, err
}
func (s *Bolt) Revoke(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(devices)
		var device identity.Device
		if json.Unmarshal(b.Get([]byte(id)), &device) != nil {
			return protocol.Err("not_found", "Device not found")
		}
		device.Revoked = true
		return b.Put([]byte(id), protocol.JSON(device))
	})
}
