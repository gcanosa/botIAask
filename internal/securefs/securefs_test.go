package securefs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMkdirPrivateTightensExistingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data", "backups")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := MkdirPrivate(dir); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("mode = %v, want 0700", fi.Mode().Perm())
	}
}
