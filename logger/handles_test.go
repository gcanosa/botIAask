package logger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func withTempLogs(t *testing.T) string {
	t.Helper()
	old := logsDir
	logsDir = filepath.Join(t.TempDir(), "logs")
	t.Cleanup(func() { CloseAll(); logsDir = old })
	CloseAll()
	return logsDir
}

func readLog(t *testing.T, dir, channel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, ChannelFileKey(channel, "net")+"_"+time.Now().Format("2006-01-02")+".log"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Lines reach disk immediately (the web tail and rollups read the same file) while the handle is reused.
func TestLogChannelEventReusesHandleAndWritesThrough(t *testing.T) {
	dir := withTempLogs(t)
	LogChannelEvent("net", "#chan", EventMessage, "alice", "one", "")
	LogChannelEvent("net", "#chan", EventAction, "bob", "waves", "")
	LogChannelEvent("net", "#other", EventMessage, "carol", "elsewhere", "")

	mu.Lock()
	open := len(logHandles)
	mu.Unlock()
	if open != 2 {
		t.Fatalf("open handles = %d, want 2 (one per channel file)", open)
	}
	got := readLog(t, dir, "#chan")
	if !strings.Contains(got, "<alice> one\n") || !strings.Contains(got, "* bob waves\n") {
		t.Fatalf("unexpected log:\n%s", got)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Fatalf("logs dir mode %v, want 0700", fi.Mode().Perm())
	}
}

// A day file deleted from under the bot must not swallow later lines for the rest of the day.
func TestLogChannelEventRecoversFromRemovedFile(t *testing.T) {
	dir := withTempLogs(t)
	LogChannelEvent("net", "#chan", EventMessage, "alice", "before", "")
	path := filepath.Join(dir, ChannelFileKey("#chan", "net")+"_"+time.Now().Format("2006-01-02")+".log")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	lastHandleGC = time.Time{} // make the next call run the sweep
	mu.Unlock()
	LogChannelEvent("net", "#chan", EventMessage, "alice", "after", "")
	LogChannelEvent("net", "#chan", EventMessage, "alice", "after2", "")
	if got := readLog(t, dir, "#chan"); !strings.Contains(got, "after2") {
		t.Fatalf("lines lost after the file was removed:\n%s", got)
	}
}

func TestIdleHandlesAreClosed(t *testing.T) {
	withTempLogs(t)
	LogChannelEvent("net", "#chan", EventMessage, "alice", "x", "")
	mu.Lock()
	for _, h := range logHandles {
		h.used = time.Now().Add(-2 * logHandleIdle)
	}
	lastHandleGC = time.Time{}
	closeIdleLocked(time.Now())
	n := len(logHandles)
	mu.Unlock()
	if n != 0 {
		t.Fatalf("idle handle kept open (%d)", n)
	}
}
