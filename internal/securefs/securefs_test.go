package securefs

import (
	"os"
	"path/filepath"
	"testing"
)

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

// Default: the historical modes, and an existing directory (an operator's volume) is left alone.
func TestDefaultModesLeaveExistingDirsAlone(t *testing.T) {
	SetPrivate(false)
	existing := filepath.Join(t.TempDir(), "logs")
	if err := os.Mkdir(existing, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := Mkdir(existing); err != nil {
		t.Fatal(err)
	}
	if got := mode(t, existing); got != 0o750 {
		t.Fatalf("existing dir mode changed to %v", got)
	}
	fresh := filepath.Join(t.TempDir(), "data", "backups")
	if err := Mkdir(fresh); err != nil {
		t.Fatal(err)
	}
	if got := mode(t, fresh); got != 0o755 {
		t.Fatalf("new dir mode %v, want 0755", got)
	}
	if FileMode() != 0o644 {
		t.Fatalf("file mode %v, want 0644", FileMode())
	}
}

func TestPrivateModeTightensDirsAndFiles(t *testing.T) {
	SetPrivate(true)
	t.Cleanup(func() { SetPrivate(false) })
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Mkdir(dir); err != nil {
		t.Fatal(err)
	}
	if got := mode(t, dir); got != 0o700 {
		t.Fatalf("mode = %v, want 0700", got)
	}
	if FileMode() != 0o600 {
		t.Fatalf("file mode %v, want 0600", FileMode())
	}
}
