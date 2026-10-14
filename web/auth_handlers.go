package web

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// handleCSRFToken issues a fresh CSRF token for the caller's existing admin
// session, so the frontend can pick one up without re-authenticating (the
// token minted at login expires after 1h, well before the 24h session).
func (s *Server) handleCSRFToken(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("admin_session")
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if _, _, err := s.authDB.ValidateSession(cookie.Value); err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	csrfToken, err := s.authDB.GenerateCSRFToken(cookie.Value)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"csrf_token": csrfToken})
}

// requireAdminCSRF checks session validity and, for mutating methods
// (POST/PUT/PATCH/DELETE), also validates the CSRF token from X-CSRF-Token header
// or csrf_token form field.
func (s *Server) requireAdminCSRF(r *http.Request) (bool, bool) {
	cookie, err := r.Cookie("admin_session")
	if err != nil {
		return false, false
	}
	_, needsChange, err := s.authDB.ValidateSession(cookie.Value)
	if err != nil {
		return false, false
	}
	if r.Method == http.MethodPost || r.Method == http.MethodPut ||
		r.Method == http.MethodPatch || r.Method == http.MethodDelete {
		if !s.csrfValid(r, cookie.Value) {
			return false, false
		}
	}
	if needsChange {
		// Forced password change: nothing but handlePasswordUpdate/logout may run until it is done.
		return false, true
	}
	return true, needsChange
}

// requireAdmin lets an authenticated dashboard admin (CSRF-checked for mutating methods) through
// and otherwise answers 401 itself; handlers do `if !s.requireAdmin(w, r) { return }`.
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if ok, _ := s.requireAdminCSRF(r); !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

// csrfValid returns true if the request carries a valid CSRF token for the
// given session token. For non-mutating methods it always returns true.
func (s *Server) csrfValid(r *http.Request, sessionToken string) bool {
	if r.Method != http.MethodPost && r.Method != http.MethodPut &&
		r.Method != http.MethodPatch && r.Method != http.MethodDelete {
		return true
	}
	csrfToken := r.Header.Get("X-CSRF-Token")
	if csrfToken == "" {
		csrfToken = r.FormValue("csrf_token")
	}
	return csrfToken != "" && s.authDB.ValidateCSRFToken(csrfToken, sessionToken)
}

func isPrivilegedAdminRole(role string) bool {
	r := strings.ToLower(strings.TrimSpace(role))
	return r == "" || r == "admin"
}

// sessionStaffInfo returns dashboard session validity, whether the user has staff (moderation) privileges, and password-change flag.
func (s *Server) sessionStaffInfo(r *http.Request) (sessionOK, staffAdmin bool, needsPasswordChange bool) {
	cookie, err := r.Cookie("admin_session")
	if err != nil {
		return false, false, false
	}
	uid, needs, err := s.authDB.ValidateSession(cookie.Value)
	if err != nil {
		return false, false, false
	}
	role, err := s.authDB.GetUserRole(uid)
	if err != nil {
		return true, false, needs
	}
	return true, isPrivilegedAdminRole(role), needs
}

// staffAdminFromRequest reports whether the request carries a valid privileged dashboard session.
// Mutating methods additionally require a valid CSRF token, so every handler that gates on this
// is CSRF-protected without having to remember requireAdminCSRF.
func (s *Server) staffAdminFromRequest(r *http.Request) bool {
	_, staff, needsChange := s.sessionStaffInfo(r)
	if !staff || needsChange {
		return false
	}
	cookie, err := r.Cookie("admin_session")
	return err == nil && s.csrfValid(r, cookie.Value)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	clientIP := GetClientIP(r, s.getConfig().Web.TrustForwardedFor)
	if !s.loginRateLimiter.IsAllowed(clientIP) {
		http.Error(w, "Too many login attempts. Please try again later.", http.StatusTooManyRequests)
		return
	}

	var creds struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&creds); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	userID, needsChange, err := s.authDB.Authenticate(creds.Username, creds.Password)
	if err != nil {
		http.Error(w, "Invalid credentials", http.StatusUnauthorized)
		return
	}
	if err := s.authDB.TouchLastLogin(userID); err != nil {
		log.Printf("last_login: %v", err)
	}

	token, err := s.authDB.CreateSession(userID)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	csrfToken, err := s.authDB.GenerateCSRFToken(token)
	if err != nil {
		log.Printf("csrf token generation: %v", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	secure := true
	if r.Header.Get("X-Forwarded-Proto") != "https" && !isLocalhost(r.Host) {
		secure = false
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "admin_session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
		Expires:  time.Now().Add(24 * time.Hour),
	})

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":               true,
		"needs_password_change": needsChange,
		"csrf_token":            csrfToken,
	})
}

// isLocalhost checks if the host is localhost or 127.0.0.1
func isLocalhost(host string) bool {
	return strings.HasPrefix(host, "localhost") || strings.HasPrefix(host, "127.0.0.1") || strings.HasPrefix(host, "[::1]")
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("admin_session")
	if err == nil {
		s.authDB.DeleteSession(cookie.Value)
	}

	secure := true
	if r.Header.Get("X-Forwarded-Proto") != "https" && !isLocalhost(r.Host) {
		secure = false
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "admin_session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("admin_session")
	if err != nil || !s.staffAdminFromRequest(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if !s.csrfValid(r, cookie.Value) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	switch r.Method {
	case http.MethodGet:
		users, err := s.authDB.GetUsers()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(users)

	case http.MethodPost:
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		if err := s.authDB.AddUser(req.Username, req.Password); err != nil {
			writeAuthError(w, err)
			return
		}
		w.WriteHeader(http.StatusCreated)

	case http.MethodPatch:
		var req struct {
			ID          string `json:"id"`
			NewPassword string `json:"new_password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		if err := s.authDB.UpdateUserPassword(req.ID, req.NewPassword); err != nil {
			writeAuthError(w, err)
			return
		}
		w.WriteHeader(http.StatusOK)

	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "ID required", http.StatusBadRequest)
			return
		}
		// Deleting yourself or the last user would lock everyone out of the dashboard.
		if uid, _, err := s.authDB.ValidateSession(cookie.Value); err == nil && strconv.Itoa(uid) == id {
			http.Error(w, "You cannot delete your own account", http.StatusBadRequest)
			return
		}
		if n, err := s.authDB.UserCount(); err == nil && n <= 1 {
			http.Error(w, "Cannot delete the last user", http.StatusBadRequest)
			return
		}
		if err := s.authDB.RemoveUser(id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)

	}
}

// writeAuthError maps a password-policy failure to 400 and anything else to a generic 500.
func writeAuthError(w http.ResponseWriter, err error) {
	var pe *PasswordPolicyError
	if errors.As(err, &pe) {
		http.Error(w, pe.Error(), http.StatusBadRequest)
		return
	}
	log.Printf("auth: %v", err)
	http.Error(w, "Internal error", http.StatusInternalServerError)
}

func (s *Server) handlePasswordUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	cookie, err := r.Cookie("admin_session")
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	userID, needsChange, err := s.authDB.ValidateSession(cookie.Value)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if !s.csrfValid(r, cookie.Value) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	var req struct {
		Password        string `json:"password"`
		CurrentPassword string `json:"current_password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	// A stolen session must not be able to lock the owner out: changing the password needs the
	// current one, except in the forced first-login change (the user is only holding the default).
	if !needsChange {
		if !s.loginRateLimiter.IsAllowed(GetClientIP(r, s.getConfig().Web.TrustForwardedFor)) {
			http.Error(w, "Too many attempts. Please try again later.", http.StatusTooManyRequests)
			return
		}
		if !s.authDB.VerifyPassword(userID, req.CurrentPassword) {
			http.Error(w, "Current password is incorrect", http.StatusForbidden)
			return
		}
	}

	if err := s.authDB.UpdatePassword(userID, req.Password); err != nil {
		writeAuthError(w, err)
		return
	}
	// A leaked/old password must not keep other sessions alive.
	s.authDB.DeleteOtherSessions(userID, cookie.Value)

	w.WriteHeader(http.StatusOK)
}
