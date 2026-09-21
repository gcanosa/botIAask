package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
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
	key, err := os.ReadFile(SecretKeyPath)
	if err != nil || len(key) != secretKeyLen {
		key = make([]byte, secretKeyLen)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(SecretKeyPath), 0700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(SecretKeyPath, key, 0600); err != nil {
			return nil, err
		}
	}
	keyBuf, keyFor = key, SecretKeyPath
	return key, nil
}

func newGCM() (cipher.AEAD, error) {
	key, err := secretKey()
	if err != nil {
		return nil, fmt.Errorf("secret key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func encryptSecret(s string) (string, error) {
	if s == "" || strings.HasPrefix(s, encPrefix) {
		return s, nil
	}
	gcm, err := newGCM()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	return encPrefix + base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(s), nil)), nil
}

// decryptSecret returns the plaintext; legacy plaintext values pass through with plain=true.
func decryptSecret(s string) (out string, plain bool, err error) {
	if !strings.HasPrefix(s, encPrefix) {
		return s, s != "", nil
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, encPrefix))
	if err != nil {
		return "", false, err
	}
	gcm, err := newGCM()
	if err != nil {
		return "", false, err
	}
	if len(data) < gcm.NonceSize() {
		return "", false, fmt.Errorf("ciphertext too short")
	}
	pt, err := gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], nil)
	if err != nil {
		return "", false, fmt.Errorf("decrypt (wrong or missing %s?): %w", SecretKeyPath, err)
	}
	return string(pt), false, nil
}

// mapSecrets applies fn to every secret field of cfg in place. Add new secret fields here.
func mapSecrets(cfg *Config, fn func(*string) error) error {
	ptrs := []*string{&cfg.Web.Auth.Password, &cfg.Flight.APIKey, &cfg.OMDB.APIKey, &cfg.GitHub.Token}
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
