package stats

import (
	"path/filepath"
	"testing"
	"time"

	"os"
)

func TestChanRollupAndReport(t *testing.T) {
	db, err := NewDatabase(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	logs := t.TempDir()
	today := time.Now().Format("2006-01-02")
	body := "[14:01:02] <alice> !weather x\n[14:02:00] <bob> hi\n[15:00:00] *** alice sets mode +o bob\n[15:01:00] *** bob changed the topic to: hey\n"
	os.WriteFile(filepath.Join(logs, "libera_linux_"+today+".log"), []byte(body), 0644)
	os.WriteFile(filepath.Join(logs, "libera_"+today+".log"), []byte("[10:00:00] <x> pm\n"), 0644) // PM log: ignored
	nets := []NetNick{{"libera", "Bot"}, {"lib", "Bot"}}
	if n := db.RollupLogs([]string{logs}, nets, "!", 2); n != 1 {
		t.Fatalf("wrote %d rows", n)
	}
	rep, err := db.ChanReport(7, "libera", "#linux")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Channels) != 1 || rep.Channels[0].Msgs != 2 || rep.Hours[14] != 2 || rep.Modes["+o"] != 1 ||
		len(rep.Cmds) != 1 || rep.Cmds[0].Name != "weather" || len(rep.Topics) != 1 || len(rep.Daily) != 7 {
		t.Fatalf("report: %+v", rep)
	}
}

// A day split across spellings (#Linux / #linux, logged before keys were lower-cased) counts
// in full, and a plain log still wins over an archived copy of the same day.
func TestChanRollupMergesSpellings(t *testing.T) {
	db, err := NewDatabase(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	logs, archive := t.TempDir(), t.TempDir()
	today := time.Now().Format("2006-01-02")
	os.WriteFile(filepath.Join(logs, "libera_Linux_"+today+".log"), []byte("[09:00:00] <alice> morning\n"), 0644)
	if _, err := os.Stat(filepath.Join(logs, "libera_linux_"+today+".log")); err == nil {
		t.Skip("case-insensitive filesystem: spellings are the same file")
	}
	os.WriteFile(filepath.Join(logs, "libera_linux_"+today+".log"), []byte("[18:00:00] <bob> evening\n"), 0644)
	os.WriteFile(filepath.Join(archive, "libera_linux_"+today+".log"), []byte("[01:00:00] <old> archived copy\n"), 0644)
	if n := db.RollupLogs([]string{logs, archive}, []NetNick{{"libera", "Bot"}}, "!", 2); n != 1 {
		t.Fatalf("wrote %d rows, want 1", n)
	}
	rep, err := db.ChanReport(7, "libera", "#linux")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Channels) != 1 || rep.Channels[0].Msgs != 2 || rep.Hours[9] != 1 || rep.Hours[18] != 1 || rep.Hours[1] != 0 {
		t.Fatalf("report: %+v", rep)
	}
}
