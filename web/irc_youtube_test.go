package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"botIAask/config"
)

// PUT replaces the network's youtube_preview list (persisted); GET returns it plus the configured channels.
func TestIRCYouTubeHandler(t *testing.T) {
	cfg := &config.Config{}
	cfg.IRC.Networks = []config.IRCNetworkConfig{{Name: "libera", Server: "irc.libera.chat", Port: 6697, Nickname: "bot",
		Channels: []config.IRChannel{{Name: "#a"}, {Name: "#b"}}}}
	s, cookie, csrf := setupNetworkEditTest(t, cfg)
	do := func(method, target, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.AddCookie(cookie)
		req.Header.Set("X-CSRF-Token", csrf)
		rec := httptest.NewRecorder()
		s.handleIRCYouTube(rec, req)
		return rec
	}
	if rec := do(http.MethodPut, "/api/irc/youtube", `{"network":"LIBERA","channels":["#a","#x","#A"]}`); rec.Code != 200 ||
		!strings.Contains(rec.Body.String(), `"channels":["#a","#x"]`) {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(http.MethodPut, "/api/irc/youtube", `{"network":"libera","channels":["#a b"]}`); rec.Code != 400 {
		t.Fatalf("bad channel accepted: %d", rec.Code)
	}
	if rec := do(http.MethodGet, "/api/irc/youtube?network=libera", ""); !strings.Contains(rec.Body.String(), `"known":["#a","#b"]`) {
		t.Fatalf("get: %s", rec.Body.String())
	}
	saved, err := config.LoadConfig(config.DefaultConfigPath)
	if err != nil || len(saved.IRC.Networks[0].YouTubePreview) != 2 {
		t.Fatalf("not persisted: %v %+v", err, saved)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/irc/youtube?network=libera", nil) // no session
	rec := httptest.NewRecorder()
	s.handleIRCYouTube(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET: %d", rec.Code)
	}
}
