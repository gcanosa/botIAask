package logger

import (
	"os"
	"path/filepath"
	"testing"
)

// Two networks sharing a channel name must not collide onto the same log file key.
func TestChannelFileKeyDistinctPerNetwork(t *testing.T) {
	a := ChannelFileKey("#general", "libera")
	b := ChannelFileKey("#general", "oftc")
	if a == b {
		t.Fatalf("expected distinct keys for different networks, got %q for both", a)
	}
	if a != "libera_general" {
		t.Errorf("got %q, want %q", a, "libera_general")
	}
	if b != "oftc_general" {
		t.Errorf("got %q, want %q", b, "oftc_general")
	}
}

func TestChannelFileKeyPMFallsBackToServerName(t *testing.T) {
	if got := ChannelFileKey("somenick", "libera"); got != "libera" {
		t.Errorf("got %q, want %q", got, "libera")
	}
}

func TestChannelFileKeyLowercasesChannel(t *testing.T) {
	if a, b := ChannelFileKey("#News", "SekurNet"), ChannelFileKey("#news", "SekurNet"); a != b || a != "SekurNet_news" {
		t.Fatalf("got %q and %q, want both %q", a, b, "SekurNet_news")
	}
}

func TestDayLogPathsFindsEverySpelling(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) { os.WriteFile(filepath.Join(dir, name), []byte("x\n"), 0o644) }
	write("net_news_2026-10-11.log")
	write("net_news_2026-10-10.log") // other day
	write("net_newsroom_2026-10-11.log")
	if got := DayLogPaths(dir, "net_news", "2026-10-11", ".log"); len(got) != 1 || filepath.Base(got[0]) != "net_news_2026-10-11.log" {
		t.Fatalf("got %v", got)
	}
	write("net_News_2026-10-11.log")
	got := DayLogPaths(dir, "net_news", "2026-10-11", ".log")
	want := 2
	if _, err := os.Stat(filepath.Join(dir, "NET_NEWS_2026-10-11.log")); err == nil {
		want = 1 // case-insensitive filesystem: both names are one file
	}
	if len(got) != want || filepath.Base(got[0]) != "net_news_2026-10-11.log" {
		t.Fatalf("got %v, want %d paths, canonical first", got, want)
	}
}
