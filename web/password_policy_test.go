package web

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"botIAask/config"
)

func newPolicyServer(t *testing.T) (*Server, int, *http.Cookie, string) {
	t.Helper()
	authDB, err := NewAuthDatabase(filepath.Join(t.TempDir(), "web_auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { authDB.Close() })
	if err := authDB.AddUser("admin", "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}
	uid, _, err := authDB.Authenticate("admin", "correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := authDB.CreateSession(uid)
	if err != nil {
		t.Fatal(err)
	}
	csrf, err := authDB.GenerateCSRFToken(tok)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{authDB: authDB, cfg: &config.Config{}, loginRateLimiter: NewLoginRateLimiter(time.Minute, 100)}
	return s, uid, &http.Cookie{Name: "admin_session", Value: tok}, csrf
}

func postJSON(s *Server, h http.HandlerFunc, path, body string, c *http.Cookie, csrf string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.AddCookie(c)
	req.Header.Set("X-CSRF-Token", csrf)
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func TestValidatePassword(t *testing.T) {
	for _, bad := range []string{"", "short", "elevenchars", strings.Repeat("x", 73)} {
		if ValidatePassword(bad) == nil {
			t.Errorf("%d-byte password accepted", len(bad))
		}
	}
	for _, ok := range []string{"twelve-chars", strings.Repeat("x", 72)} {
		if err := ValidatePassword(ok); err != nil {
			t.Errorf("%d-byte password refused: %v", len(ok), err)
		}
	}
}

func TestPasswordUpdateNeedsCurrentPasswordAndPolicy(t *testing.T) {
	s, uid, cookie, csrf := newPolicyServer(t)

	// Wrong / missing current password: refused, password unchanged.
	if rec := postJSON(s, s.handlePasswordUpdate, "/api/users/password", `{"password":"another-long-password"}`, cookie, csrf); rec.Code != http.StatusForbidden {
		t.Fatalf("missing current password: status %d", rec.Code)
	}
	// Right current password but a weak new one: 400.
	if rec := postJSON(s, s.handlePasswordUpdate, "/api/users/password", `{"password":"weak","current_password":"correct-horse-battery"}`, cookie, csrf); rec.Code != http.StatusBadRequest {
		t.Fatalf("weak password: status %d", rec.Code)
	}
	if !s.authDB.VerifyPassword(uid, "correct-horse-battery") {
		t.Fatal("password changed despite refusals")
	}
	// Right current password and a good new one: ok.
	if rec := postJSON(s, s.handlePasswordUpdate, "/api/users/password", `{"password":"another-long-password","current_password":"correct-horse-battery"}`, cookie, csrf); rec.Code != http.StatusOK {
		t.Fatalf("valid change: status %d body %s", rec.Code, rec.Body.String())
	}
	if !s.authDB.VerifyPassword(uid, "another-long-password") {
		t.Fatal("new password not stored")
	}
}

// The forced first-login change has no old password to ask for, only the policy applies.
func TestForcedPasswordChangeSkipsCurrentPassword(t *testing.T) {
	s, uid, cookie, csrf := newPolicyServer(t)
	if _, err := s.authDB.db.Exec("UPDATE web_users SET needs_password_change = 1 WHERE id = ?", uid); err != nil {
		t.Fatal(err)
	}
	if rec := postJSON(s, s.handlePasswordUpdate, "/api/users/password", `{"password":"brand-new-password"}`, cookie, csrf); rec.Code != http.StatusOK {
		t.Fatalf("forced change: status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestDeleteUserGuards(t *testing.T) {
	s, uid, cookie, csrf := newPolicyServer(t)
	del := func(id string) int {
		req := httptest.NewRequest(http.MethodDelete, "/api/users?id="+id, nil)
		req.AddCookie(cookie)
		req.Header.Set("X-CSRF-Token", csrf)
		rec := httptest.NewRecorder()
		s.handleUsers(rec, req)
		return rec.Code
	}
	if c := del(strconv.Itoa(uid)); c != http.StatusBadRequest {
		t.Fatalf("deleting yourself / last user: status %d, want 400", c)
	}
	if n, _ := s.authDB.UserCount(); n != 1 {
		t.Fatalf("user count = %d", n)
	}
}

func TestPurgeExpiredSessions(t *testing.T) {
	s, uid, _, _ := newPolicyServer(t)
	if _, err := s.authDB.db.Exec("INSERT INTO web_sessions (token, user_id, expires_at) VALUES ('old', ?, datetime('now','-1 day'))", uid); err != nil {
		t.Fatal(err)
	}
	if err := s.authDB.PurgeExpired(); err != nil {
		t.Fatal(err)
	}
	var n int
	s.authDB.db.QueryRow("SELECT COUNT(*) FROM web_sessions WHERE token = 'old'").Scan(&n)
	if n != 0 {
		t.Fatal("expired session survived the purge")
	}
	if _, _, err := s.authDB.ValidateSession(func() string {
		var tk string
		s.authDB.db.QueryRow("SELECT token FROM web_sessions LIMIT 1").Scan(&tk)
		return tk
	}()); err != nil {
		t.Fatalf("live session was purged: %v", err)
	}
}
