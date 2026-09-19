package logger

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestChannelActivity(t *testing.T) {
	old := logsDir
	logsDir = t.TempDir()
	defer func() { logsDir = old }()
	name := ChannelFileKey("#c", "net") + "_" + time.Now().Format("2006-01-02") + ".log"
	body := "[14:01:02] <alice> hi\n[14:05:00] * bob waves\n[03:00:00] <Bot> spam\n[14:06:00] *** x has joined #c\n"
	if err := os.WriteFile(filepath.Join(logsDir, name), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	a := ChannelActivity("net", "#c", 7, "bot")
	if a.Total != 2 || a.Hours[14] != 2 || a.Hours[3] != 0 || a.Nicks["alice"] != 1 || a.Days != 1 {
		t.Fatalf("got %+v", a)
	}
}
