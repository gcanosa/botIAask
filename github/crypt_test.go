package github

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	c, err := NewCryptor(filepath.Join(t.TempDir(), "key"))
	if err != nil {
		t.Fatalf("NewCryptor: %v", err)
	}

	enc, err := c.Encrypt("ghp_supersecrettoken")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if enc == "ghp_supersecrettoken" {
		t.Fatal("ciphertext must not equal plaintext")
	}
	dec, err := c.Decrypt(enc)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if dec != "ghp_supersecrettoken" {
		t.Fatalf("expected round-trip, got %q", dec)
	}
}

func TestEncryptDecrypt_EmptyPassthrough(t *testing.T) {
	c, err := NewCryptor(filepath.Join(t.TempDir(), "key"))
	if err != nil {
		t.Fatalf("NewCryptor: %v", err)
	}
	enc, err := c.Encrypt("")
	if err != nil || enc != "" {
		t.Fatalf("expected empty passthrough, got %q err=%v", enc, err)
	}
	dec, err := c.Decrypt("")
	if err != nil || dec != "" {
		t.Fatalf("expected empty passthrough, got %q err=%v", dec, err)
	}
}

func TestDecrypt_WrongKeyFails(t *testing.T) {
	c1, _ := NewCryptor(filepath.Join(t.TempDir(), "key1"))
	c2, _ := NewCryptor(filepath.Join(t.TempDir(), "key2"))

	enc, err := c1.Encrypt("secret")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := c2.Decrypt(enc); err == nil {
		t.Fatal("expected decrypt with wrong key to fail")
	}
}

func TestNewCryptor_PersistsAndReloadsSameKey(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "subdir_does_not_exist_key")
	// Use a path in an existing dir so WriteFile succeeds.
	keyPath = filepath.Join(t.TempDir(), "key")

	c1, err := NewCryptor(keyPath)
	if err != nil {
		t.Fatalf("NewCryptor (first): %v", err)
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat key file: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("expected key file mode 0600, got %v", info.Mode().Perm())
	}

	c2, err := NewCryptor(keyPath)
	if err != nil {
		t.Fatalf("NewCryptor (second): %v", err)
	}

	enc, err := c1.Encrypt("value")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	dec, err := c2.Decrypt(enc)
	if err != nil || dec != "value" {
		t.Fatalf("expected second Cryptor to load the same persisted key, got %q err=%v", dec, err)
	}
}
