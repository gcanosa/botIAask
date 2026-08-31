package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"botIAask/config"
	"botIAask/rss"
)

// TestRSSSettingsSaveIsCSRFProtectedAndPersists exercises the exact path the
// "URL Shortener Service" dropdown in the dashboard drives: POST
// /api/rss/settings must be rejected without a CSRF token (the original bug
// report), and once a valid token is attached it must both apply in memory
// and persist url_shortener to config.yaml on disk.
func TestRSSSettingsSaveIsCSRFProtectedAndPersists(t *testing.T) {
	tmpDir := t.TempDir()
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Mkdir(filepath.Join(tmpDir, "config"), 0755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer os.Chdir(origWD)

	cfg := &config.Config{}
	cfg.RSS.URLShortener = "is.gd"
	cfg.IRC.Networks = []config.IRCNetworkConfig{{Name: "testnet", Server: "irc.example.com"}}

	db, err := rss.NewDatabase(filepath.Join(tmpDir, "rss.db"))
	if err != nil {
		t.Fatalf("rss.NewDatabase: %v", err)
	}
	defer db.Close()
	fetcher := rss.NewFetcher(cfg, nil, db)

	authDB, err := NewAuthDatabase(filepath.Join(tmpDir, "web_auth.db"))
	if err != nil {
		t.Fatalf("NewAuthDatabase: %v", err)
	}
	defer authDB.Close()
	if err := authDB.AddUser("admin", "hunter2pass"); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	userID, _, err := authDB.Authenticate("admin", "hunter2pass")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	sessionToken, err := authDB.CreateSession(userID)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	s := &Server{cfg: cfg, authDB: authDB, rssFetcher: fetcher}
	cookie := &http.Cookie{Name: "admin_session", Value: sessionToken}

	body := `{"interval_minutes":30,"retention_count":120,"feed_urls":["https://example.com/rss"],"announce_to_irc":true,"url_shortener":"v.gd"}`

	// No CSRF token: must be rejected, exactly like the original bug report.
	reqNoToken := httptest.NewRequest(http.MethodPost, "/api/rss/settings", strings.NewReader(body))
	reqNoToken.Header.Set("Content-Type", "application/json")
	reqNoToken.AddCookie(cookie)
	recNoToken := httptest.NewRecorder()
	s.handleRSSSettings(recNoToken, reqNoToken)
	if recNoToken.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without CSRF token, got %d: %s", recNoToken.Code, recNoToken.Body.String())
	}
	if cfg.RSS.URLShortener != "is.gd" {
		t.Fatalf("config must not change on a rejected request, got %q", cfg.RSS.URLShortener)
	}

	// Mint a token via the same endpoint the frontend now calls, then retry.
	tokenReq := httptest.NewRequest(http.MethodGet, "/api/csrf-token", nil)
	tokenReq.AddCookie(cookie)
	tokenRec := httptest.NewRecorder()
	s.handleCSRFToken(tokenRec, tokenReq)
	if tokenRec.Code != http.StatusOK {
		t.Fatalf("handleCSRFToken: %d %s", tokenRec.Code, tokenRec.Body.String())
	}
	csrfToken := strings.Split(strings.Split(tokenRec.Body.String(), `"csrf_token":"`)[1], `"`)[0]

	reqWithToken := httptest.NewRequest(http.MethodPost, "/api/rss/settings", strings.NewReader(body))
	reqWithToken.Header.Set("Content-Type", "application/json")
	reqWithToken.AddCookie(cookie)
	reqWithToken.Header.Set("X-CSRF-Token", csrfToken)
	recWithToken := httptest.NewRecorder()
	s.handleRSSSettings(recWithToken, reqWithToken)
	if recWithToken.Code != http.StatusOK {
		t.Fatalf("expected 200 with valid CSRF token, got %d: %s", recWithToken.Code, recWithToken.Body.String())
	}

	if cfg.RSS.URLShortener != "v.gd" {
		t.Fatalf("in-memory config not updated: got %q, want v.gd", cfg.RSS.URLShortener)
	}

	onDisk, err := config.LoadConfig(config.DefaultConfigPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if onDisk.RSS.URLShortener != "v.gd" {
		t.Fatalf("config.yaml on disk has url_shortener=%q, want v.gd", onDisk.RSS.URLShortener)
	}
}
