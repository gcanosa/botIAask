package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"botIAask/config"
	"botIAask/github"
)

// setupGitHubTrackerTest mirrors setupNetworkEditTest (irc_network_edit_test.go): a
// minimal Server + admin session in a temp cwd with a config/ dir, plus a real
// github.Fetcher (own temp DB/key) so handlers that call s.githubFetcher.EncryptToken
// don't nil-deref.
func setupGitHubTrackerTest(t *testing.T, cfg *config.Config) (*Server, *http.Cookie, string) {
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

	ghDB, err := github.NewDatabase(filepath.Join(tmpDir, "github_seen.db"))
	if err != nil {
		t.Fatalf("github.NewDatabase: %v", err)
	}
	t.Cleanup(func() { ghDB.Close() })
	cryptor, err := github.NewCryptor(filepath.Join(tmpDir, "github_secret.key"))
	if err != nil {
		t.Fatalf("github.NewCryptor: %v", err)
	}
	fetcher := github.NewFetcher(cfg, stubGitHubBot{}, ghDB, cryptor)

	s := &Server{cfg: cfg, authDB: authDB, githubFetcher: fetcher, rehashExt: func(string, bool) error { return nil }}
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

type stubGitHubBot struct{}

func (stubGitHubBot) Broadcast(channels []string, message string) {}
func (stubGitHubBot) IsConnected() bool                           { return false }

func doGitHubReposRequest(t *testing.T, s *Server, cookie *http.Cookie, csrfToken, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, "/api/github/repos", reader)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrfToken)
	rec := httptest.NewRecorder()
	s.handleGitHubTrackerRepos(rec, req)
	return rec
}

func doGitHubRepoEdit(t *testing.T, s *Server, cookie *http.Cookie, csrfToken, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/api/github/repos/edit", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrfToken)
	rec := httptest.NewRecorder()
	s.handleGitHubTrackerRepoEdit(rec, req)
	return rec
}

func TestGitHubRepos_AddPersistsCiphertextNotPlaintext(t *testing.T) {
	cfg := &config.Config{IRC: config.IRCConfig{Networks: []config.IRCNetworkConfig{{Name: "libera", Server: "irc.libera.chat", Port: 6697, Nickname: "bot"}}}}
	s, cookie, csrfToken := setupGitHubTrackerTest(t, cfg)

	body := `{"owner":"anthropics","repo":"claude-code","channels":["libera:#dev"],"token":"ghp_supersecret"}`
	rec := doGitHubReposRequest(t, s, cookie, csrfToken, http.MethodPost, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("add: %d %s", rec.Code, rec.Body.String())
	}

	if len(cfg.GitHubTracker.Repos) != 1 {
		t.Fatalf("expected 1 repo persisted, got %d", len(cfg.GitHubTracker.Repos))
	}
	stored := cfg.GitHubTracker.Repos[0]
	if stored.TokenEncrypted == "" || stored.TokenEncrypted == "ghp_supersecret" {
		t.Fatalf("expected token stored encrypted (not plaintext, not empty), got %q", stored.TokenEncrypted)
	}
}

func TestGitHubRepos_GETNeverReturnsToken(t *testing.T) {
	cfg := &config.Config{
		IRC: config.IRCConfig{Networks: []config.IRCNetworkConfig{{Name: "libera", Server: "irc.libera.chat", Port: 6697, Nickname: "bot"}}},
		GitHubTracker: config.GitHubTrackerConfig{Repos: []config.GitHubTrackerRepoConfig{
			{Owner: "anthropics", Repo: "claude-code", TokenEncrypted: "opaque-ciphertext"},
		}},
	}
	s, cookie, csrfToken := setupGitHubTrackerTest(t, cfg)

	rec := doGitHubReposRequest(t, s, cookie, csrfToken, http.MethodGet, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "opaque-ciphertext") || strings.Contains(rec.Body.String(), `"token"`) {
		t.Fatalf("expected GET response to never include the token/ciphertext, got: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"has_token":true`) {
		t.Fatalf("expected has_token:true flag, got: %s", rec.Body.String())
	}
}

func TestGitHubRepoEdit_BlankTokenPreservesExisting(t *testing.T) {
	cfg := &config.Config{
		IRC: config.IRCConfig{Networks: []config.IRCNetworkConfig{{Name: "libera", Server: "irc.libera.chat", Port: 6697, Nickname: "bot"}}},
		GitHubTracker: config.GitHubTrackerConfig{Repos: []config.GitHubTrackerRepoConfig{
			{Owner: "anthropics", Repo: "claude-code", TokenEncrypted: "existing-ciphertext"},
		}},
	}
	s, cookie, csrfToken := setupGitHubTrackerTest(t, cfg)

	body := `{"owner":"anthropics","repo":"claude-code","channels":["libera:#dev"]}`
	rec := doGitHubRepoEdit(t, s, cookie, csrfToken, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body.String())
	}
	if cfg.GitHubTracker.Repos[0].TokenEncrypted != "existing-ciphertext" {
		t.Fatalf("expected ciphertext preserved when token omitted, got %q", cfg.GitHubTracker.Repos[0].TokenEncrypted)
	}
}

func TestGitHubRepoEdit_NonBlankTokenReplaces(t *testing.T) {
	cfg := &config.Config{
		IRC: config.IRCConfig{Networks: []config.IRCNetworkConfig{{Name: "libera", Server: "irc.libera.chat", Port: 6697, Nickname: "bot"}}},
		GitHubTracker: config.GitHubTrackerConfig{Repos: []config.GitHubTrackerRepoConfig{
			{Owner: "anthropics", Repo: "claude-code", TokenEncrypted: "existing-ciphertext"},
		}},
	}
	s, cookie, csrfToken := setupGitHubTrackerTest(t, cfg)

	body := `{"owner":"anthropics","repo":"claude-code","token":"new-token-value"}`
	rec := doGitHubRepoEdit(t, s, cookie, csrfToken, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body.String())
	}
	got := cfg.GitHubTracker.Repos[0].TokenEncrypted
	if got == "existing-ciphertext" || got == "new-token-value" || got == "" {
		t.Fatalf("expected ciphertext replaced with a new (encrypted, non-plaintext) value, got %q", got)
	}

	dec, err := s.githubFetcher.DecryptToken(got)
	if err != nil || dec != "new-token-value" {
		t.Fatalf("expected new token to decrypt correctly, got %q err=%v", dec, err)
	}
}

// TestGitHubRepos_AddDoesNotPanicWithoutBot guards joinAnnounceChannels' nil-bot no-op:
// none of the setup helpers wire up a real *irc.Bot (would require a live connection), so
// every add/edit request already exercises this path — it must not panic or fail the save.
func TestGitHubRepos_AddDoesNotPanicWithoutBot(t *testing.T) {
	cfg := &config.Config{IRC: config.IRCConfig{Networks: []config.IRCNetworkConfig{{Name: "libera", Server: "irc.libera.chat", Port: 6697, Nickname: "bot"}}}}
	s, cookie, csrfToken := setupGitHubTrackerTest(t, cfg)

	body := `{"owner":"anthropics","repo":"claude-code","channels":["libera:#not-joined-yet"]}`
	rec := doGitHubReposRequest(t, s, cookie, csrfToken, http.MethodPost, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("add: %d %s", rec.Code, rec.Body.String())
	}
}

func TestGitHubRepoEdit_ExplicitEmptyTokenClears(t *testing.T) {
	cfg := &config.Config{
		IRC: config.IRCConfig{Networks: []config.IRCNetworkConfig{{Name: "libera", Server: "irc.libera.chat", Port: 6697, Nickname: "bot"}}},
		GitHubTracker: config.GitHubTrackerConfig{Repos: []config.GitHubTrackerRepoConfig{
			{Owner: "anthropics", Repo: "claude-code", TokenEncrypted: "existing-ciphertext"},
		}},
	}
	s, cookie, csrfToken := setupGitHubTrackerTest(t, cfg)

	body := `{"owner":"anthropics","repo":"claude-code","token":""}`
	rec := doGitHubRepoEdit(t, s, cookie, csrfToken, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body.String())
	}
	if cfg.GitHubTracker.Repos[0].TokenEncrypted != "" {
		t.Fatalf("expected explicit empty token to clear the stored ciphertext, got %q", cfg.GitHubTracker.Repos[0].TokenEncrypted)
	}
}
