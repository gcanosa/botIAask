package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The PID file is only "in use" while a process holds its lock; a leftover file naming a live
// (reused) pid must not count, otherwise -mode stop would signal an unrelated process.
func TestPIDFileInUseFollowsTheLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bot.pid")
	if PIDFileInUse(path) {
		t.Fatal("missing pid file reported in use")
	}
	// stale file that names a perfectly alive pid (ours) but nobody holds the lock
	if err := os.WriteFile(path, []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if PIDFileInUse(path) {
		t.Fatal("unlocked pid file reported in use")
	}
	if err := WritePIDFile(path); err != nil {
		t.Fatal(err)
	}
	if !PIDFileInUse(path) {
		t.Fatal("locked pid file not reported in use")
	}
	if pid, err := ReadPIDFile(path); err != nil || pid != os.Getpid() {
		t.Fatalf("ReadPIDFile = %d, %v", pid, err)
	}
	// a second instance cannot take the lock
	if os.Getenv("PID_LOCK_CHILD") == "1" {
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestPIDFileSecondInstance", "-test.count=1")
	cmd.Env = append(os.Environ(), "PID_LOCK_FILE="+path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("second instance should be refused: %v\n%s", err, out)
	}
}

func TestPIDFileSecondInstance(t *testing.T) {
	path := os.Getenv("PID_LOCK_FILE")
	if path == "" {
		t.Skip("helper for TestPIDFileInUseFollowsTheLock")
	}
	if err := WritePIDFile(path); err == nil {
		t.Fatal("second WritePIDFile succeeded while the lock is held")
	}
}
