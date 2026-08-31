package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// TestCSRFTokenEndpointUnblocksMutatingSaves reproduces the "Save failed"
// report: a mutating admin request with only the session cookie (no
// X-CSRF-Token header, matching what the frontend historically sent) must be
// rejected by requireAdminCSRF, and /api/csrf-token must hand back a token
// that then lets the same request through.
func TestCSRFTokenEndpointUnblocksMutatingSaves(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "web_auth.db")
	authDB, err := NewAuthDatabase(dbPath)
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

	s := &Server{authDB: authDB}
	cookie := &http.Cookie{Name: "admin_session", Value: sessionToken}

	// Without a CSRF token the mutating request must be refused.
	reqNoToken := httptest.NewRequest(http.MethodPost, "/api/rss/settings", strings.NewReader("{}"))
	reqNoToken.AddCookie(cookie)
	if ok, _ := s.requireAdminCSRF(reqNoToken); ok {
		t.Fatal("expected requireAdminCSRF to fail without a CSRF token")
	}

	// /api/csrf-token issues one for the current session.
	tokenReq := httptest.NewRequest(http.MethodGet, "/api/csrf-token", nil)
	tokenReq.AddCookie(cookie)
	rec := httptest.NewRecorder()
	s.handleCSRFToken(rec, tokenReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("handleCSRFToken status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "csrf_token") {
		t.Fatalf("expected csrf_token in response, got %s", rec.Body.String())
	}

	var body struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.CSRFToken == "" {
		t.Fatal("csrf_token was empty")
	}

	// The same mutating request now succeeds with the token attached.
	reqWithToken := httptest.NewRequest(http.MethodPost, "/api/rss/settings", strings.NewReader("{}"))
	reqWithToken.AddCookie(cookie)
	reqWithToken.Header.Set("X-CSRF-Token", body.CSRFToken)
	if ok, _ := s.requireAdminCSRF(reqWithToken); !ok {
		t.Fatal("expected requireAdminCSRF to succeed with a valid CSRF token")
	}
}
