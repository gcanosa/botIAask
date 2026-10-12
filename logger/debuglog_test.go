package logger

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStartDebugLog(t *testing.T) {
	old, oldOut := logsDir, log.Writer()
	logsDir = t.TempDir()
	defer func() { logsDir = old; log.SetOutput(oldOut) }()

	if err := StartDebugLog(); err != nil {
		t.Fatal(err)
	}
	log.Printf("reconnecting to libera")
	b, err := os.ReadFile(filepath.Join(DebugDir(), "bot_"+time.Now().Format("2006-01-02")+".log"))
	if err != nil {
		t.Fatal(err)
	}
	if s := string(b); !strings.Contains(s, "process start") || !strings.Contains(s, "reconnecting to libera") {
		t.Fatalf("debug log missing lines: %q", s)
	}
}
