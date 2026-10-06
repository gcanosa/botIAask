package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSecretKeyNotOverwrittenOnBadLength(t *testing.T) {
	dir := t.TempDir()
	old, oldBuf, oldFor := SecretKeyPath, keyBuf, keyFor
	t.Cleanup(func() { SecretKeyPath, keyBuf, keyFor = old, oldBuf, oldFor })
	keyBuf, keyFor = nil, ""
	SecretKeyPath = filepath.Join(dir, "k.key")
	bad := append(make([]byte, secretKeyLen), '\n')
	if err := os.WriteFile(SecretKeyPath, bad, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := secretKey(); err == nil {
		t.Fatal("expected error for wrong-length key")
	}
	got, _ := os.ReadFile(SecretKeyPath)
	if len(got) != len(bad) {
		t.Fatal("existing key file was overwritten")
	}
}
