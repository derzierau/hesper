package e2e

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/derzierau/hesper/relay/pkg/devicekey"
)

// KeyFile is the controller's static key in its state directory (mode
// 0600). It never leaves the machine.
const KeyFile = "e2e.key"

// Identity is a controller's static X25519 key and its binding to the
// device key: Binding is the device key's signature of
// BindingDigest(Public).
type Identity struct {
	Private, Public []byte
	Binding         string
	path            string
}

type identityFile struct {
	Version int    `json:"version"`
	Private string `json:"private"`
	Binding string `json:"binding,omitempty"`
}

// LoadIdentity reads dir/e2e.key, creating the key on first use (two
// processes creating it at once both end up with the first one).
func LoadIdentity(dir string) (*Identity, error) {
	path := filepath.Join(dir, KeyFile)
	data, err := devicekey.ReadPrivateFile(path)
	if errors.Is(err, os.ErrNotExist) {
		key, e := ecdh.X25519().GenerateKey(rand.Reader)
		if e != nil {
			return nil, e
		}
		raw, _ := json.Marshal(identityFile{Version: 1, Private: base64.StdEncoding.EncodeToString(key.Bytes())})
		if e := os.MkdirAll(dir, 0700); e != nil {
			return nil, e
		}
		if e := devicekey.WriteNewPrivateFile(path, append(raw, '\n')); e != nil {
			return nil, e
		}
		data, err = devicekey.ReadPrivateFile(path)
	}
	if err != nil {
		return nil, err
	}
	var f identityFile
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 {
		return nil, fmt.Errorf("%s is not a Hesper end-to-end key", path)
	}
	private, err := base64.StdEncoding.DecodeString(f.Private)
	if err != nil || len(private) != 32 {
		return nil, fmt.Errorf("%s holds no valid X25519 key", path)
	}
	key, err := ecdh.X25519().NewPrivateKey(private)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &Identity{Private: private, Public: key.PublicKey().Bytes(), Binding: f.Binding, path: path}, nil
}

// Bind signs the static key with the device key (not the strong one: no
// Touch ID) and stores the signature next to the key.
func (id *Identity) Bind(signer devicekey.Signer) error {
	if signer == nil {
		return errors.New("no device keys to bind the end-to-end key to")
	}
	digest := BindingDigest(id.Public)
	sig, err := signer.Sign(digest[:], devicekey.Options{})
	if err != nil {
		return err
	}
	id.Binding = base64.StdEncoding.EncodeToString(sig)
	if id.path == "" {
		return nil
	}
	raw, _ := json.Marshal(identityFile{Version: 1, Private: base64.StdEncoding.EncodeToString(id.Private), Binding: id.Binding})
	return writeAtomic(id.path, append(raw, '\n'))
}

// VerifyBinding checks that binding is key's signature of static.
func VerifyBinding(key *ecdsa.PublicKey, static []byte, binding string) error {
	sig, err := base64.StdEncoding.Strict().DecodeString(binding)
	if err != nil || len(sig) == 0 || len(sig) > 80 {
		return errors.New("e2e: no valid binding signature")
	}
	digest := BindingDigest(static)
	if key == nil || !ecdsa.VerifyASN1(key, digest[:], sig) {
		return errors.New("e2e: the end-to-end key is not signed by the approved device key")
	}
	return nil
}

// writeAtomic replaces path with data (mode 0600) through a synced
// temporary file and rename.
func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err := temp.Chmod(0600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}
