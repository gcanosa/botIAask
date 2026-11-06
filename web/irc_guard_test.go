package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"botIAask/config"
)

// PUT replaces the network's guard list (validated, persisted); GET returns it plus the configured channels.
func TestIRCGuardHandler(t *testing.T) {
	cfg := &config.Config{}
	cfg.IRC.Networks = []config.IRCNetworkConfig{{Name: "libera", Server: "irc.libera.chat", Port: 6697, Nickname: "bot",
		Channels: []config.IRChannel{{Name: "#a"}, {Name: "#b"}}}}
	s, cookie, csrf := setupNetworkEditTest(t, cfg)
	do := func(method, target, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.AddCookie(cookie)
		req.Header.Set("X-CSRF-Token", csrf)
		rec := httptest.NewRecorder()
		s.handleIRCGuard(rec, req)
		return rec
	}
	if rec := do(http.MethodPut, "/api/irc/guard", `{"network":"LIBERA","guards":[{"channel":"#a","joins":3,"moderate":true},{"channel":"#x"}]}`); rec.Code != 200 ||
		!strings.Contains(rec.Body.String(), `"guards":[{"channel":"#a","joins":3,"moderate":true,"interval":0},{"channel":"#x","joins":5,"moderate":false,"interval":0}]`) {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	for _, bad := range []string{`[{"channel":"#a b"}]`, `[{"channel":"*"}]`, `[{"channel":"#a","joins":51}]`, `[{"channel":"#a","interval":2}]`} {
		if rec := do(http.MethodPut, "/api/irc/guard", `{"network":"libera","guards":`+bad+`}`); rec.Code != 400 {
			t.Fatalf("%s accepted: %d", bad, rec.Code)
		}
	}
	if rec := do(http.MethodGet, "/api/irc/guard?network=libera", ""); !strings.Contains(rec.Body.String(), `"known":["#a","#b"]`) {
		t.Fatalf("get: %s", rec.Body.String())
	}
	saved, err := config.LoadConfig(config.DefaultConfigPath)
	if err != nil || len(saved.IRC.Networks[0].Guard) != 2 {
		t.Fatalf("not persisted: %v %+v", err, saved)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/irc/guard?network=libera", nil) // no session
	rec := httptest.NewRecorder()
	s.handleIRCGuard(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET: %d", rec.Code)
	}
}
