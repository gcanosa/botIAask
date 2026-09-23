package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecureHeadersAndBodyCap(t *testing.T) {
	var readErr error
	h := secure(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(strings.Repeat("a", maxBodyBytes+1)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if readErr == nil {
		t.Fatal("oversized body was not rejected")
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("missing security headers: %v", rec.Header())
	}

	// /upload keeps its own (larger) limit.
	readErr = nil
	req = httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader(strings.Repeat("a", maxBodyBytes+1)))
	h.ServeHTTP(httptest.NewRecorder(), req)
	if readErr != nil {
		t.Fatalf("/upload should not be capped by middleware: %v", readErr)
	}
}

func TestGetClientIPUsesRightmostForwarded(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.9")
	if got := GetClientIP(r, true); got != "203.0.113.9" {
		t.Fatalf("got %q, want proxy-appended rightmost entry", got)
	}
	if got := GetClientIP(r, false); got != "10.0.0.1" {
		t.Fatalf("untrusted: got %q", got)
	}
}
