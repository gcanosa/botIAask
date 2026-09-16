package github

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"os"
)

const secretKeyLen = 32 // AES-256

// Cryptor encrypts/decrypts GitHub PATs at rest with AES-256-GCM. The key is a random
// 32-byte value generated once and persisted to disk (0600) — there is no human-chosen
// passphrase to strengthen, so no KDF (argon2/scrypt) is involved; the key IS the key.
type Cryptor struct {
	key []byte
}

// NewCryptor loads the encryption key from keyPath, generating and persisting one if it
// doesn't exist yet (mirrors the auto-generated admin password in web/auth_db.go).
func NewCryptor(keyPath string) (*Cryptor, error) {
	key, err := os.ReadFile(keyPath)
	if err == nil && len(key) == secretKeyLen {
		return &Cryptor{key: key}, nil
	}

	key = make([]byte, secretKeyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("github: generate secret key: %w", err)
	}
	if err := os.WriteFile(keyPath, key, 0600); err != nil {
		return nil, fmt.Errorf("github: persist secret key: %w", err)
	}
	log.Printf("[SECURITY] Generated GitHub tracker PAT encryption key at %s (mode 0600) — back this up; losing it makes stored tokens unrecoverable (re-enter them via the dashboard)", keyPath)
	return &Cryptor{key: key}, nil
}

// Encrypt returns the base64-encoded AES-256-GCM ciphertext of plaintext. An empty
// plaintext (no token configured) passes through unchanged.
func (c *Cryptor) Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt reverses Encrypt. An empty input passes through unchanged.
func (c *Cryptor) Decrypt(encoded string) (string, error) {
	if encoded == "" {
		return "", nil
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("github: decode ciphertext: %w", err)
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(data) < gcm.NonceSize() {
		return "", fmt.Errorf("github: ciphertext too short")
	}
	nonce, ciphertext := data[:gcm.NonceSize()], data[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("github: decrypt: %w", err)
	}
	return string(plaintext), nil
}
