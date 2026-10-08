// Package transfer encrypts files a controller sends to a host through the
// relay, so the relay only ever carries ciphertext. The host publishes a
// long-lived X25519 public key; a sender uses one ephemeral key per upload.
// Each file gets its own AES-256-GCM key from HKDF-SHA256 over the shared
// secret, bound to the upload ID and file name. Chunks are sealed with a
// counter nonce and associated data naming the upload, file, chunk index and
// whether it is the last chunk, so chunks cannot be reordered, moved between
// files or uploads, or truncated.
//
// Downloads (bring back) reverse the roles: the requester sends a fresh
// ephemeral public key, the host seals with a key derived from its long-lived
// private key and that ephemeral key, and the requester opens with its
// ephemeral private key and the host key it pinned. Only the requester can
// read a download and only the host can have sealed it. Downloads use their
// own label, so a chunk of one direction never opens in the other.
package transfer

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ChunkSize is the plaintext size of every chunk but the last, which may be
// shorter (or empty). Sealed and base64-encoded it stays below the 1 MiB
// message limit.
const ChunkSize = 512 * 1024

// Overhead is the GCM tag added to every chunk.
const Overhead = 16

// wire names: kept as "ghosty-transfer-v1" and "ghosty-download-v1" until the
// next relay deploy.
const (
	label         = "ghosty-transfer-v1"
	downloadLabel = "ghosty-download-v1"
)

// GenerateKey returns a new X25519 private key.
func GenerateKey() (*ecdh.PrivateKey, error) { return ecdh.X25519().GenerateKey(rand.Reader) }

// LoadOrCreateKey reads a base64 private key from path, creating it (mode
// 0600, parent 0700) on first use. A key readable by others is refused.
func LoadOrCreateKey(path string) (*ecdh.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		key, err := GenerateKey()
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return nil, err
		}
		_, err = f.WriteString(base64.StdEncoding.EncodeToString(key.Bytes()) + "\n")
		if err == nil {
			err = f.Sync()
		}
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			os.Remove(path)
			return nil, err
		}
		return key, nil
	}
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("transfer key %s must not be readable by others (chmod 600)", path)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil {
		return nil, fmt.Errorf("invalid transfer key %s", path)
	}
	return ecdh.X25519().NewPrivateKey(raw)
}

// EncodePublicKey is the form published as the snapshot's transferKey.
func EncodePublicKey(key *ecdh.PublicKey) string {
	return base64.StdEncoding.EncodeToString(key.Bytes())
}

// ParsePublicKey reverses EncodePublicKey.
func ParsePublicKey(value string) (*ecdh.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(raw) != 32 {
		return nil, errors.New("invalid X25519 public key")
	}
	return ecdh.X25519().NewPublicKey(raw)
}

// Sender seals the files of one upload for one host.
type Sender struct {
	ephemeral *ecdh.PrivateKey
	host      *ecdh.PublicKey
	upload    string
	files     map[string]cipher.AEAD
}

func NewSender(host *ecdh.PublicKey, upload string) (*Sender, error) {
	ephemeral, err := GenerateKey()
	if err != nil {
		return nil, err
	}
	return &Sender{ephemeral: ephemeral, host: host, upload: upload, files: map[string]cipher.AEAD{}}, nil
}

// EphemeralKey is sent with each file's last chunk (`epk`).
func (s *Sender) EphemeralKey() string { return EncodePublicKey(s.ephemeral.PublicKey()) }

// Seal encrypts chunk index of file name. Every chunk but the last must hold
// exactly ChunkSize bytes.
func (s *Sender) Seal(name string, index uint64, last bool, plain []byte) ([]byte, error) {
	if len(plain) > ChunkSize || (!last && len(plain) != ChunkSize) {
		return nil, errors.New("chunk has the wrong size")
	}
	aead := s.files[name]
	if aead == nil {
		var err error
		aead, err = fileKey(label, s.ephemeral, s.host, s.ephemeral.PublicKey(), s.host, s.upload, name)
		if err != nil {
			return nil, err
		}
		s.files[name] = aead
	}
	return aead.Seal(nil, nonce(index), plain, associated(label, s.upload, name, index, last)), nil
}

// Opener decrypts one file on the host.
type Opener struct {
	aead         cipher.AEAD
	upload, name string
}

func NewOpener(key *ecdh.PrivateKey, epk, upload, name string) (*Opener, error) {
	ephemeral, err := ParsePublicKey(epk)
	if err != nil {
		return nil, err
	}
	aead, err := fileKey(label, key, ephemeral, ephemeral, key.PublicKey(), upload, name)
	if err != nil {
		return nil, err
	}
	return &Opener{aead: aead, upload: upload, name: name}, nil
}

// Open authenticates and decrypts chunk index.
func (o *Opener) Open(index uint64, last bool, sealed []byte) ([]byte, error) {
	plain, err := o.aead.Open(nil, nonce(index), sealed, associated(label, o.upload, o.name, index, last))
	if err != nil {
		return nil, errors.New("chunk failed authentication")
	}
	return plain, nil
}

// Download seals (on the host) or opens (on the requester) the files of one
// download. It is safe for concurrent use.
type Download struct {
	private         *ecdh.PrivateKey
	peer            *ecdh.PublicKey
	ephemeral, host *ecdh.PublicKey
	id              string
	mu              sync.Mutex
	files           map[string]cipher.AEAD
}

// NewHostDownload is the host's side: key is its long-lived transfer key,
// requester the requester's ephemeral public key (base64), id the download.
func NewHostDownload(key *ecdh.PrivateKey, requester, id string) (*Download, error) {
	ephemeral, err := ParsePublicKey(requester)
	if err != nil {
		return nil, err
	}
	return &Download{private: key, peer: ephemeral, ephemeral: ephemeral, host: key.PublicKey(), id: id, files: map[string]cipher.AEAD{}}, nil
}

// NewRequesterDownload is the requester's side: ephemeral is the key whose
// public half went with the request, host the host key it pinned.
func NewRequesterDownload(ephemeral *ecdh.PrivateKey, host *ecdh.PublicKey, id string) *Download {
	return &Download{private: ephemeral, peer: host, ephemeral: ephemeral.PublicKey(), host: host, id: id, files: map[string]cipher.AEAD{}}
}

func (d *Download) aead(name string) (cipher.AEAD, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if aead := d.files[name]; aead != nil {
		return aead, nil
	}
	aead, err := fileKey(downloadLabel, d.private, d.peer, d.ephemeral, d.host, d.id, name)
	if err == nil {
		d.files[name] = aead
	}
	return aead, err
}

// Seal encrypts chunk index of file name. Every chunk but the last must hold
// exactly ChunkSize bytes.
func (d *Download) Seal(name string, index uint64, last bool, plain []byte) ([]byte, error) {
	if len(plain) > ChunkSize || (!last && len(plain) != ChunkSize) {
		return nil, errors.New("chunk has the wrong size")
	}
	aead, err := d.aead(name)
	if err != nil {
		return nil, err
	}
	return aead.Seal(nil, nonce(index), plain, associated(downloadLabel, d.id, name, index, last)), nil
}

// Open authenticates and decrypts chunk index of file name.
func (d *Download) Open(name string, index uint64, last bool, sealed []byte) ([]byte, error) {
	aead, err := d.aead(name)
	if err != nil {
		return nil, err
	}
	plain, err := aead.Open(nil, nonce(index), sealed, associated(downloadLabel, d.id, name, index, last))
	if err != nil {
		return nil, errors.New("chunk failed authentication")
	}
	return plain, nil
}

// fileKey derives the AES-256-GCM key for one file. The salt binds both public
// keys; the info binds the direction, the upload (or download) and file name.
func fileKey(label string, private *ecdh.PrivateKey, peer, ephemeral, host *ecdh.PublicKey, upload, name string) (cipher.AEAD, error) {
	shared, err := private.ECDH(peer)
	if err != nil {
		return nil, err
	}
	salt := append(append([]byte{}, ephemeral.Bytes()...), host.Bytes()...)
	key, err := hkdf.Key(sha256.New, shared, salt, label+"\x00"+upload+"\x00"+name, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func nonce(index uint64) []byte {
	n := make([]byte, 12)
	binary.BigEndian.PutUint64(n[4:], index)
	return n
}

func associated(label, upload, name string, index uint64, last bool) []byte {
	ad := []byte(label + "\x00" + upload + "\x00" + name + "\x00")
	ad = binary.BigEndian.AppendUint64(ad, index)
	if last {
		return append(ad, 1)
	}
	return append(ad, 0)
}
