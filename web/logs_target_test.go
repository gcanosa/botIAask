package web

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"botIAask/config"
)

func TestValidLogTarget(t *testing.T) {
	cfg := &config.Config{}
	cfg.IRC.Networks = []config.IRCNetworkConfig{{Name: "libera"}}
	cases := []struct {
		ch, net string
		ok      bool
	}{
		{"#go", "", true},
		{"#go", "libera", true},
		{"&local", "libera", true},
		{"nick", "libera", false},     // would map to the private-message log
		{"", "libera", false},         //
		{"#go", "../../etc/x", false}, // traversal via network
		{"#go", "unknown-net", false},
	}
	for _, c := range cases {
		if got := validLogTarget(cfg, c.ch, c.net); got != c.ok {
			t.Errorf("validLogTarget(%q,%q)=%v want %v", c.ch, c.net, got, c.ok)
		}
	}
}

func TestIsNetworkLogKey(t *testing.T) {
	cfg := &config.Config{}
	cfg.IRC.Networks = []config.IRCNetworkConfig{{Name: "irc.libera.chat"}}
	if !isNetworkLogKey(cfg, "irc.libera.chat") {
		t.Fatal("network name is the PM log key and must be refused as a channel fallback")
	}
	if isNetworkLogKey(cfg, "go") {
		t.Fatal("ordinary channel key misclassified")
	}
}

func TestIsKeyedChannel(t *testing.T) {
	cfg := &config.Config{}
	cfg.IRC.Networks = []config.IRCNetworkConfig{
		{Name: "a", Channels: []config.IRChannel{{Name: "#pub"}, {Name: "#staff", Password: "k"}}},
		{Name: "b", Channels: []config.IRChannel{{Name: "#staff"}}},
	}
	if !isKeyedChannel(cfg, "#staff", "a") || !isKeyedChannel(cfg, "#STAFF", "") {
		t.Fatal("keyed channel not detected")
	}
	if isKeyedChannel(cfg, "#staff", "b") || isKeyedChannel(cfg, "#pub", "a") {
		t.Fatal("unkeyed channel misclassified")
	}
}

func TestLogCatalog_RetentionAndCaseFold(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll("logs/archive", 0o755); err != nil {
		t.Fatal(err)
	}
	day := func(n int) string { return time.Now().AddDate(0, 0, n).Format("2006-01-02") }
	for _, name := range []string{
		"logs/SekurNet_news_" + day(-1) + ".log",          // server spelled it lower-case
		"logs/libera_gone_" + day(-1) + ".log",            // parted, still within retention
		"logs/archive/libera_old_" + day(-30) + ".log.gz", // parted, aged out
	} {
		if err := os.WriteFile(name, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{}
	cfg.Logger.RotationDays = 7
	cfg.IRC.Networks = []config.IRCNetworkConfig{
		{Name: "SekurNet", Channels: []config.IRChannel{{Name: "#News"}}},
		{Name: "libera"},
	}
	s := &Server{cfg: cfg}
	rec := httptest.NewRecorder()
	s.handleLogCatalog(rec, httptest.NewRequest("GET", "/api/logs/catalog", nil))
	var resp logCatalogResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	got := map[string]logChannelEntry{}
	for _, c := range resp.Channels {
		got[c.Network+" "+c.Label] = c
	}
	if c, ok := got["SekurNet #News"]; !ok || !c.Joined || len(c.DatesWithLogs) != 1 || len(got) != 2 {
		t.Fatalf("want #News (joined, merged with #news file) and libera #gone only, got %+v", resp.Channels)
	}
	if _, ok := got["libera #gone"]; !ok {
		t.Fatalf("parted channel within retention missing: %+v", resp.Channels)
	}
}
