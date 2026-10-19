package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// SecretKeyPath is the AES-256 key used to encrypt secrets in config.yaml. It is the same
// file (and ciphertext format) the GitHub tracker uses for PAT encryption, so there is one
// key to back up. Losing it makes "enc:" values unrecoverable — re-enter them.
var SecretKeyPath = "data/github_secret.key"

const (
	encPrefix    = "enc:"
	secretKeyLen = 32
)

var (
	keyMu  sync.Mutex
	keyBuf []byte
	keyFor string
)

func secretKey() ([]byte, error) {
	keyMu.Lock()
	defer keyMu.Unlock()
	if keyBuf != nil && keyFor == SecretKeyPath {
		return keyBuf, nil
	}
	key, err := LoadKey(SecretKeyPath)
	if err != nil {
		return nil, err
	}
	keyBuf, keyFor = key, SecretKeyPath
	return key, nil
}

// LoadKey reads the 32-byte AES-256 key at path, creating a random one (mode 0600, O_EXCL) when the
// file does not exist yet. An existing file of the wrong size or one that cannot be read is an
// error: it is never overwritten, since it may be the only copy of the key.
func LoadKey(path string) ([]byte, error) {
	key, err := os.ReadFile(path)
	if err == nil && len(key) != secretKeyLen {
		return nil, fmt.Errorf("secret key %s has %d bytes, want %d (fix or restore it; not regenerating)", path, len(key), secretKeyLen)
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("read secret key %s: %w", path, err)
	}
	if err == nil {
		return key, nil
	}
	key = make([]byte, secretKeyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if err := writeNewKey(path, key); err != nil {
		return nil, err
	}
	log.Printf("[SECURITY] Generated encryption key at %s (mode 0600) — back it up; losing it makes stored secrets unrecoverable (re-enter them)", path)
	return key, nil
}

func gcmFor(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Seal encrypts plaintext with key (AES-256-GCM) and returns base64(nonce || ciphertext). Empty
// input stays empty (no secret configured).
func Seal(key []byte, plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	gcm, err := gcmFor(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(plaintext), nil)), nil
}

// Open reverses Seal. Empty input stays empty.
func Open(key []byte, encoded string) (string, error) {
	if encoded == "" {
		return "", nil
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decode ciphertext: %w", err)
	}
	gcm, err := gcmFor(key)
	if err != nil {
		return "", err
	}
	if len(data) < gcm.NonceSize() {
		return "", fmt.Errorf("ciphertext too short")
	}
	pt, err := gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}
	return string(pt), nil
}

func encryptSecret(s string) (string, error) {
	if s == "" || strings.HasPrefix(s, encPrefix) {
		return s, nil
	}
	key, err := secretKey()
	if err != nil {
		return "", fmt.Errorf("secret key: %w", err)
	}
	enc, err := Seal(key, s)
	if err != nil {
		return "", err
	}
	return encPrefix + enc, nil
}

// decryptSecret returns the plaintext; legacy plaintext values pass through with plain=true.
func decryptSecret(s string) (out string, plain bool, err error) {
	if !strings.HasPrefix(s, encPrefix) {
		return s, s != "", nil
	}
	key, err := secretKey()
	if err != nil {
		return "", false, fmt.Errorf("secret key: %w", err)
	}
	out, err = Open(key, strings.TrimPrefix(s, encPrefix))
	if err != nil {
		return "", false, fmt.Errorf("%w (wrong or missing %s?)", err, SecretKeyPath)
	}
	return out, false, nil
}

// mapSecrets applies fn to every secret field of cfg in place. Add new secret fields here.
func mapSecrets(cfg *Config, fn func(*string) error) error {
	ptrs := []*string{&cfg.Web.Auth.Password, &cfg.Flight.APIKey, &cfg.OMDB.APIKey, &cfg.HamQTH.Password, &cfg.GitHub.Token}
	for i := range cfg.IRC.Networks {
		n := &cfg.IRC.Networks[i]
		ptrs = append(ptrs, &n.Services.Password, &n.Services.NickServPassword, &n.Services.ClientCert)
		for j := range n.Channels {
			ptrs = append(ptrs, &n.Channels[j].Password)
		}
	}
	for _, p := range ptrs {
		if err := fn(p); err != nil {
			return err
		}
	}
	return nil
}

// encryptedCopy returns a copy of cfg with secrets encrypted; cfg itself is untouched.
func encryptedCopy(cfg *Config) (*Config, error) {
	c := *cfg
	c.IRC.Networks = append([]IRCNetworkConfig(nil), cfg.IRC.Networks...)
	for i := range c.IRC.Networks {
		c.IRC.Networks[i].Channels = append([]IRChannel(nil), c.IRC.Networks[i].Channels...)
	}
	err := mapSecrets(&c, func(p *string) (err error) { *p, err = encryptSecret(*p); return })
	return &c, err
}

// decryptSecrets decrypts cfg in place and reports whether any plaintext (legacy) secret was seen.
func decryptSecrets(cfg *Config) (sawPlain bool, err error) {
	err = mapSecrets(cfg, func(p *string) error {
		out, plain, err := decryptSecret(*p)
		sawPlain = sawPlain || plain
		*p = out
		return err
	})
	return
}

// writeNewKey creates the key file exclusively so an existing key is never clobbered.
func writeNewKey(path string, key []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err := f.Write(key); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
