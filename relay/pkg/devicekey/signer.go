package devicekey

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Signer holds a controller's two device keys.
type Signer interface {
	// PublicKey returns the device key, or with strong the strong key.
	PublicKey(strong bool) (*ecdsa.PublicKey, error)
	// Sign returns the DER ECDSA signature of a 32-byte SHA-256 digest.
	Sign(digest []byte, opts Options) ([]byte, error)
	// Hardware reports whether the keys cannot leave this machine
	// (Secure Enclave).
	Hardware() bool
}

// File names in the state directory.
const (
	DeviceFile = "device.key"
	StrongFile = "device-strong.key"
)

// StateDir is $HESPER_STATE_DIR, else ~/.local/state/hesper.
func StateDir() string {
	if dir := os.Getenv("HESPER_STATE_DIR"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local/state/hesper")
}

// SoftwareMode reports HESPER_KEYS_SOFTWARE=1.
func SoftwareMode() bool { return os.Getenv("HESPER_KEYS_SOFTWARE") == "1" }

// ErrNoHelper means this Mac has no hesper-keys helper installed.
var ErrNoHelper = errors.New("hesper-keys is not installed (run install.sh, or make -C relay keys)")

// Default returns this machine's signer: software keys in StateDir with
// HESPER_KEYS_SOFTWARE=1 or off macOS, else the hesper-keys helper.
func Default() (Signer, error) {
	if SoftwareMode() || runtime.GOOS != "darwin" {
		return &Software{Dir: StateDir()}, nil
	}
	path := FindHelper()
	if path == "" {
		return nil, ErrNoHelper
	}
	return &Helper{Path: path, Dir: StateDir()}, nil
}

// FindHelper looks for hesper-keys in $HESPER_KEYS, ~/.local/lib/hesper and
// next to this executable.
func FindHelper() string {
	candidates := []string{os.Getenv("HESPER_KEYS")}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".local/lib/hesper/hesper-keys"))
	}
	if self, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(self), "hesper-keys"))
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if info, err := os.Stat(c); err == nil && info.Mode().IsRegular() && info.Mode()&0111 != 0 {
			return c
		}
	}
	return ""
}

// Software keeps both keys as PEM PKCS#8 files (mode 0600) in Dir, the
// format hesper-keys uses with HESPER_KEYS_SOFTWARE=1. Keys are created on
// first use.
type Software struct {
	Dir  string
	mu   sync.Mutex
	keys [2]*ecdsa.PrivateKey
}

func (s *Software) Hardware() bool { return false }

func (s *Software) key(strong bool) (*ecdsa.PrivateKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := 0
	name := DeviceFile
	if strong {
		i, name = 1, StrongFile
	}
	if s.keys[i] != nil {
		return s.keys[i], nil
	}
	if err := os.MkdirAll(s.Dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(s.Dir, name)
	data, err := ReadPrivateFile(path)
	if errors.Is(err, os.ErrNotExist) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, err
		}
		der, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return nil, err
		}
		if err := WriteNewPrivateFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})); err != nil {
			return nil, err
		}
		data, err = ReadPrivateFile(path)
		if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	block, rest := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, fmt.Errorf("%s is not a software key (a Secure Enclave key needs the hesper-keys helper)", path)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, fmt.Errorf("%s is not a P-256 key", path)
	}
	s.keys[i] = key
	return key, nil
}

func (s *Software) PublicKey(strong bool) (*ecdsa.PublicKey, error) {
	key, err := s.key(strong)
	if err != nil {
		return nil, err
	}
	return &key.PublicKey, nil
}

func (s *Software) Sign(digest []byte, opts Options) ([]byte, error) {
	if len(digest) != 32 {
		return nil, invalid("digest must be 32 bytes")
	}
	key, err := s.key(opts.Strong)
	if err != nil {
		return nil, err
	}
	return ecdsa.SignASN1(rand.Reader, key, digest)
}

// Helper signs through the hesper-keys executable (Secure Enclave keys on a
// Mac). Each signature is one short process: the strong key's Touch ID
// prompt appears for the process that asked for it.
type Helper struct {
	Path    string
	Dir     string
	mu      sync.Mutex
	public  [2]*ecdsa.PublicKey
	timeout time.Duration
}

// Hardware is false when the helper runs with HESPER_KEYS_SOFTWARE=1.
func (h *Helper) Hardware() bool { return !SoftwareMode() }

func (h *Helper) run(stdin []byte, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, h.Path, args...)
	cmd.Env = append(os.Environ(), "HESPER_STATE_DIR="+h.Dir)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return "", fmt.Errorf("hesper-keys %s: %s", args[0], message)
	}
	return strings.TrimSpace(stdout.String()), nil
}

func (h *Helper) PublicKey(strong bool) (*ecdsa.PublicKey, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	i := 0
	args := []string{"public"}
	if strong {
		i, args = 1, append(args, "--strong")
	}
	if h.public[i] != nil {
		return h.public[i], nil
	}
	out, err := h.run(nil, 30*time.Second, args...)
	if err != nil {
		return nil, err
	}
	key, _, err := ParsePublicKey(out)
	if err != nil {
		return nil, err
	}
	h.public[i] = key
	return key, nil
}

func (h *Helper) Sign(digest []byte, opts Options) ([]byte, error) {
	if len(digest) != 32 {
		return nil, invalid("digest must be 32 bytes")
	}
	args := []string{"sign"}
	timeout := 30 * time.Second
	if opts.Strong {
		// The user has to find the Touch ID sensor.
		args = append(args, "--strong", "--reason", opts.Reason)
		timeout = 2 * time.Minute
	}
	if h.timeout > 0 {
		timeout = h.timeout
	}
	out, err := h.run([]byte(hex.EncodeToString(digest)+"\n"), timeout, args...)
	if err != nil {
		return nil, err
	}
	sig, err := base64.StdEncoding.Strict().DecodeString(out)
	if err != nil || len(sig) == 0 || len(sig) > 80 {
		return nil, fmt.Errorf("hesper-keys sign printed no signature")
	}
	return sig, nil
}

// ReadPrivateFile reads a small file that must be a regular file of this
// user that nobody else can read or write (mode 0600 or stricter).
func ReadPrivateFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("%s is accessible to other users; expected mode 0600", path)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return nil, fmt.Errorf("%s belongs to another user", path)
	}
	if info.Size() > 4<<20 {
		return nil, fmt.Errorf("%s is too large", path)
	}
	return os.ReadFile(path)
}

// WriteNewPrivateFile stores data at path (mode 0600) only if nothing is
// there yet: two processes creating a key at once both end up with the
// first one.
func WriteNewPrivateFile(path string, data []byte) error {
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
	if err := os.Link(temp.Name(), path); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return nil
}
