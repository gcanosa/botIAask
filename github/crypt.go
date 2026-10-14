package github

import (
	"botIAask/config"
)

// Cryptor encrypts/decrypts GitHub PATs at rest with AES-256-GCM. The key is a random 32-byte value
// generated once and persisted to disk (0600) — there is no human-chosen passphrase to strengthen,
// so no KDF is involved; the key IS the key. The primitives live in config (the same key also
// protects the secrets in config.yaml); this type only binds them to one key file.
type Cryptor struct {
	key []byte
}

// NewCryptor loads the encryption key from keyPath, generating and persisting one if it
// doesn't exist yet. A key file of the wrong size is an error, never silently replaced.
func NewCryptor(keyPath string) (*Cryptor, error) {
	key, err := config.LoadKey(keyPath)
	if err != nil {
		return nil, err
	}
	return &Cryptor{key: key}, nil
}

// Encrypt returns the base64-encoded AES-256-GCM ciphertext of plaintext. An empty plaintext (no
// token configured) passes through unchanged.
func (c *Cryptor) Encrypt(plaintext string) (string, error) { return config.Seal(c.key, plaintext) }

// Decrypt reverses Encrypt. An empty input passes through unchanged.
func (c *Cryptor) Decrypt(encoded string) (string, error) { return config.Open(c.key, encoded) }
