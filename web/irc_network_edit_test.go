package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"botIAask/config"
)

// setupNetworkEditTest builds a minimal Server + admin session, in a temp cwd with a
// config/ dir so config.SaveConfig's DefaultConfigPath resolves, matching the pattern in
// rss_settings_persist_test.go. Returns the server, an authenticated cookie, and a CSRF token.
func setupNetworkEditTest(t *testing.T, cfg *config.Config) (*Server, *http.Cookie, string) {
	t.Helper()
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
	t.Cleanup(func() { os.Chdir(origWD) })

	authDB, err := NewAuthDatabase(filepath.Join(tmpDir, "web_auth.db"))
	if err != nil {
		t.Fatalf("NewAuthDatabase: %v", err)
	}
	t.Cleanup(func() { authDB.Close() })
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

	s := &Server{cfg: cfg, authDB: authDB, rehashExt: func(string, bool) error { return nil }}
	cookie := &http.Cookie{Name: "admin_session", Value: sessionToken}

	tokenReq := httptest.NewRequest(http.MethodGet, "/api/csrf-token", nil)
	tokenReq.AddCookie(cookie)
	tokenRec := httptest.NewRecorder()
	s.handleCSRFToken(tokenRec, tokenReq)
	if tokenRec.Code != http.StatusOK {
		t.Fatalf("handleCSRFToken: %d %s", tokenRec.Code, tokenRec.Body.String())
	}
	csrfToken := strings.Split(strings.Split(tokenRec.Body.String(), `"csrf_token":"`)[1], `"`)[0]

	return s, cookie, csrfToken
}

func doNetworkEdit(t *testing.T, s *Server, cookie *http.Cookie, csrfToken, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/irc/networks/edit", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrfToken)
	rec := httptest.NewRecorder()
	s.handleIRCNetworkEdit(rec, req)
	return rec
}

// TestIRCNetworkEdit_QuitMessagePreservedWhenOmitted guards the data-loss bug: the
// dashboard's edit form never sent quit_message, so every edit silently erased it. Omitting
// the field now means "leave as configured".
func TestIRCNetworkEdit_QuitMessagePreservedWhenOmitted(t *testing.T) {
	cfg := &config.Config{IRC: config.IRCConfig{Networks: []config.IRCNetworkConfig{
		{Name: "libera", Server: "irc.libera.chat", Port: 6697, Nickname: "bot", QuitMessage: "custom bye"},
	}}}
	s, cookie, csrfToken := setupNetworkEditTest(t, cfg)

	body := `{"name":"libera","server":"irc.libera.chat","port":6697,"use_ssl":true,"nickname":"bot"}`
	rec := doNetworkEdit(t, s, cookie, csrfToken, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body.String())
	}

	if got := cfg.IRC.Networks[0].QuitMessage; got != "custom bye" {
		t.Fatalf("quit_message wiped: got %q, want %q preserved", got, "custom bye")
	}
}

// TestIRCNetworkEdit_EnabledPreservedWhenOmitted guards the analogous bug for `enabled`: a
// client that omits the field (nil) must not silently re-enable a disabled network.
func TestIRCNetworkEdit_EnabledPreservedWhenOmitted(t *testing.T) {
	f := false
	cfg := &config.Config{IRC: config.IRCConfig{Networks: []config.IRCNetworkConfig{
		{Name: "libera", Server: "irc.libera.chat", Port: 6697, Nickname: "bot", Enabled: &f},
	}}}
	s, cookie, csrfToken := setupNetworkEditTest(t, cfg)

	body := `{"name":"libera","server":"irc.libera.chat","port":6697,"use_ssl":true,"nickname":"bot"}`
	rec := doNetworkEdit(t, s, cookie, csrfToken, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body.String())
	}

	if got := cfg.IRC.Networks[0].IsEnabled(); got {
		t.Fatal("enabled was silently flipped to true by an edit that omitted the field")
	}
}

// TestIRCNetworkEdit_SASLPasswordPreservedWhenBlank guards the write-only-password UX: a
// blank password on edit must keep the stored one, not erase it (the dashboard never reads
// the password back to round-trip it).
func TestIRCNetworkEdit_SASLPasswordPreservedWhenBlank(t *testing.T) {
	cfg := &config.Config{IRC: config.IRCConfig{Networks: []config.IRCNetworkConfig{
		{Name: "libera", Server: "irc.libera.chat", Port: 6697, Nickname: "bot",
			Services: config.ServicesConfig{Enabled: true, Username: "reguser", Password: "supersecret"}},
	}}}
	s, cookie, csrfToken := setupNetworkEditTest(t, cfg)

	// Change only the username; password field sent blank, as the dashboard form does.
	body := `{"name":"libera","server":"irc.libera.chat","port":6697,"use_ssl":true,"nickname":"bot","sasl":{"enabled":true,"username":"reguser2","password":""}}`
	rec := doNetworkEdit(t, s, cookie, csrfToken, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body.String())
	}

	svc := cfg.IRC.Networks[0].Services
	if svc.Password != "supersecret" {
		t.Fatalf("password wiped: got %q, want preserved", svc.Password)
	}
	if svc.Username != "reguser2" {
		t.Fatalf("username not updated: got %q", svc.Username)
	}
}

// TestIRCNetworkEdit_TLSSkipVerifyPreservedWhenOmitted checks the newly-added field follows
// the same nil-means-unchanged convention as enabled/quit_message.
func TestIRCNetworkEdit_TLSSkipVerifyPreservedWhenOmitted(t *testing.T) {
	cfg := &config.Config{IRC: config.IRCConfig{Networks: []config.IRCNetworkConfig{
		{Name: "libera", Server: "irc.libera.chat", Port: 6697, Nickname: "bot", TLSSkipVerify: true},
	}}}
	s, cookie, csrfToken := setupNetworkEditTest(t, cfg)

	body := `{"name":"libera","server":"irc.libera.chat","port":6697,"use_ssl":true,"nickname":"bot"}`
	rec := doNetworkEdit(t, s, cookie, csrfToken, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body.String())
	}

	if !cfg.IRC.Networks[0].TLSSkipVerify {
		t.Fatal("tls_skip_verify was silently cleared by an edit that omitted the field")
	}
}
