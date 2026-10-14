package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	if !config.IsKeyedChannel(cfg, "#staff", "a") || !config.IsKeyedChannel(cfg, "#STAFF", "") {
		t.Fatal("keyed channel not detected")
	}
	if config.IsKeyedChannel(cfg, "#staff", "b") || config.IsKeyedChannel(cfg, "#pub", "a") {
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

// A day split across spellings (logged before keys were lower-cased) is returned in full,
// interleaved by timestamp.
func TestReadDayLog_MergesSpellings(t *testing.T) {
	t.Chdir(t.TempDir())
	os.MkdirAll("logs/archive", 0o755)
	os.WriteFile("logs/net_News_2026-10-11.log", []byte("[09:00:00] <a> one\n[11:00:00] <a> three\n"), 0o644)
	if _, err := os.Stat("logs/net_news_2026-10-11.log"); err == nil {
		t.Skip("case-insensitive filesystem: spellings are the same file")
	}
	os.WriteFile("logs/net_news_2026-10-11.log", []byte("[10:00:00] <b> two\n[12:00:00] <b> four\n"), 0o644)
	lines, truncated, archived, err := readDayLog(logsDir, []string{"net_news"}, "2026-10-11", 100)
	if err != nil || truncated || archived {
		t.Fatalf("err=%v truncated=%v archived=%v", err, truncated, archived)
	}
	want := "[09:00:00] <a> one|[10:00:00] <b> two|[11:00:00] <a> three|[12:00:00] <b> four"
	if got := strings.Join(lines, "|"); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if lines, _, _, _ := readDayLog(logsDir, []string{"net_news"}, "2026-10-11", 3); len(lines) != 3 || lines[0] != "[10:00:00] <b> two" {
		t.Fatalf("maxLines: %v", lines)
	}
}

// The live stream must not drop a line the logger was still writing when it hit EOF.
func TestLogStream_PartialLineNotDropped(t *testing.T) {
	t.Chdir(t.TempDir())
	os.MkdirAll("logs", 0o755)
	cfg := &config.Config{}
	cfg.IRC.Networks = []config.IRCNetworkConfig{{Name: "net", Channels: []config.IRChannel{{Name: "#chan"}}}}
	s := &Server{cfg: cfg}
	today := time.Now().Format("2006-01-02")
	path := "logs/net_chan_" + today + ".log"
	os.WriteFile(path, []byte("[09:00:00] <a> first\n[09:00:01] <a> sec"), 0o644)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/api/logs/stream?channel=%23chan&network=net", nil).WithContext(ctx)
	rec := &syncRecorder{ResponseRecorder: httptest.NewRecorder()}
	done := make(chan struct{})
	go func() { s.handleLogStream(rec, req); close(done) }()
	time.Sleep(300 * time.Millisecond)
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("ond\n[09:00:02] <a> third\n")
	f.Close()
	time.Sleep(1500 * time.Millisecond)
	cancel()
	<-done

	body := rec.String()
	for _, want := range []string{"data: [09:00:00] <a> first\n", "data: [09:00:01] <a> second\n", "data: [09:00:02] <a> third\n"} {
		if strings.Count(body, want) != 1 {
			t.Fatalf("want exactly one %q in stream:\n%s", want, body)
		}
	}
	if strings.Contains(body, "<a> sec\n") {
		t.Fatalf("partial line was sent:\n%s", body)
	}
}

// syncRecorder guards the recorder: the handler writes from its goroutine while the test reads.
type syncRecorder struct {
	mu sync.Mutex
	*httptest.ResponseRecorder
}

func (r *syncRecorder) Write(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ResponseRecorder.Write(b)
}

func (r *syncRecorder) Flush() {}

func (r *syncRecorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Body.String()
}

// Opening today's live view replays lines logged under an older spelling of the channel too.
func TestLogStream_ReplaysOtherSpellings(t *testing.T) {
	t.Chdir(t.TempDir())
	os.MkdirAll("logs", 0o755)
	today := time.Now().Format("2006-01-02")
	os.WriteFile("logs/net_Chan_"+today+".log", []byte("[09:00:00] <a> before restart\n"), 0o644)
	if _, err := os.Stat("logs/net_chan_" + today + ".log"); err == nil {
		t.Skip("case-insensitive filesystem: spellings are the same file")
	}
	os.WriteFile("logs/net_chan_"+today+".log", []byte("[10:00:00] <b> after restart\n"), 0o644)
	cfg := &config.Config{}
	cfg.IRC.Networks = []config.IRCNetworkConfig{{Name: "net", Channels: []config.IRChannel{{Name: "#Chan"}}}}
	s := &Server{cfg: cfg}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	rec := &syncRecorder{ResponseRecorder: httptest.NewRecorder()}
	s.handleLogStream(rec, httptest.NewRequest("GET", "/api/logs/stream?channel=%23Chan&network=net", nil).WithContext(ctx))
	body := rec.String()
	i, j := strings.Index(body, "before restart"), strings.Index(body, "after restart")
	if i < 0 || j < 0 || i > j {
		t.Fatalf("want both lines in time order:\n%s", body)
	}
}

// The bot's process log is listed and served to admins only.
func TestDebugLog_AdminOnly(t *testing.T) {
	t.Chdir(t.TempDir())
	today := time.Now().Format("2006-01-02")
	os.MkdirAll("logs/debug", 0o755)
	os.WriteFile("logs/debug/bot_"+today+".log", []byte("2026/10/12 10:00:00 irc[net]: connected\n"), 0o644)

	authDB, err := NewAuthDatabase(filepath.Join(t.TempDir(), "web_auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer authDB.Close()
	authDB.AddUser("admin", "hunter2-pass-ok")
	uid, _, _ := authDB.Authenticate("admin", "hunter2-pass-ok")
	tok, _ := authDB.CreateSession(uid)
	s := &Server{cfg: &config.Config{}, authDB: authDB}

	get := func(h func(http.ResponseWriter, *http.Request), url string, admin bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", url, nil)
		if admin {
			req.AddCookie(&http.Cookie{Name: "admin_session", Value: tok})
		}
		rec := httptest.NewRecorder()
		h(rec, req)
		return rec
	}
	hist := "/api/logs/history?debug=1&date=" + today
	if rec := get(s.handleLogHistory, hist, false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous debug history: got %d", rec.Code)
	}
	if rec := get(s.handleLogHistory, hist, true); rec.Code != 200 || !strings.Contains(rec.Body.String(), "connected") {
		t.Fatalf("admin debug history: %d %s", rec.Code, rec.Body)
	}
	for admin, want := range map[bool]bool{false: false, true: true} {
		var resp logCatalogResponse
		json.NewDecoder(get(s.handleLogCatalog, "/api/logs/catalog", admin).Body).Decode(&resp)
		found := false
		for _, c := range resp.Channels {
			found = found || (c.Debug && len(c.DatesWithLogs) == 1)
		}
		if found != want {
			t.Fatalf("admin=%v: debug entry listed=%v, want %v (%+v)", admin, found, want, resp.Channels)
		}
	}
}
