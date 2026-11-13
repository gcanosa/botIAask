package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"botIAask/config"
)

func TestRedditSubsRoundTrip(t *testing.T) {
	cfg := &config.Config{IRC: config.IRCConfig{Networks: []config.IRCNetworkConfig{{Name: "libera", Server: "irc.libera.chat", Port: 6697, Nickname: "bot"}}}}
	s, cookie, csrf := setupGitHubTrackerTest(t, cfg)
	do := func(method, url, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, url, strings.NewReader(body))
		req.AddCookie(cookie)
		req.Header.Set("X-CSRF-Token", csrf)
		rec := httptest.NewRecorder()
		s.handleRedditSubs(rec, req)
		return rec
	}

	if rec := do(http.MethodPost, "/api/reddit/subs", `{"name":"r/golang","channels":["libera:#go"]}`); rec.Code != http.StatusOK {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	if got := cfg.Reddit.Subreddits; len(got) != 1 || got[0].Name != "golang" || got[0].Channels[0] != "libera:#go" {
		t.Fatalf("after add: %+v", got)
	}
	for _, body := range []string{
		`{"name":"golang"}`,                         // duplicate
		`{"name":"https://reddit.com/r/x"}`,         // URL
		`{"name":"rust","channels":["oftc:#rust"]}`, // unknown network
	} {
		if rec := do(http.MethodPost, "/api/reddit/subs", body); rec.Code == http.StatusOK {
			t.Errorf("%s accepted", body)
		}
	}
	if len(cfg.Reddit.Subreddits) != 1 {
		t.Fatalf("rejected add left config changed: %+v", cfg.Reddit.Subreddits)
	}
	if rec := do(http.MethodPatch, "/api/reddit/subs", `{"name":"GOLANG","channels":[]}`); rec.Code != http.StatusOK || len(cfg.Reddit.Subreddits[0].Channels) != 0 {
		t.Fatalf("edit: %d %s %+v", rec.Code, rec.Body, cfg.Reddit.Subreddits)
	}
	if rec := do(http.MethodDelete, "/api/reddit/subs?name=golang", ""); rec.Code != http.StatusOK || len(cfg.Reddit.Subreddits) != 0 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
}
