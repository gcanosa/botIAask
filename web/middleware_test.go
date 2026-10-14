package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func TestConnLimiterWrap(t *testing.T) {
	l := newConnLimiter(2, 3)
	release := make(chan struct{})
	h := l.wrap(func(w http.ResponseWriter, r *http.Request) { <-release }, func(r *http.Request) string { return r.RemoteAddr })

	start := func(addr string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/s", nil)
		req.RemoteAddr = addr
		done := make(chan struct{})
		go func() { h(rec, req); close(done) }()
		select {
		case <-done: // rejected immediately
		case <-time.After(50 * time.Millisecond): // holding the slot
			rec.Code = 0
		}
		return rec
	}
	if r := start("1.1.1.1"); r.Code != 0 {
		t.Fatalf("first stream rejected: %d", r.Code)
	}
	if r := start("1.1.1.1"); r.Code != 0 {
		t.Fatalf("second stream rejected: %d", r.Code)
	}
	if r := start("1.1.1.1"); r.Code != http.StatusTooManyRequests {
		t.Fatalf("third stream from one IP should be 429, got %d", r.Code)
	}
	if r := start("2.2.2.2"); r.Code != 0 {
		t.Fatalf("other IP rejected: %d", r.Code)
	}
	if r := start("3.3.3.3"); r.Code != http.StatusTooManyRequests {
		t.Fatalf("global cap (3) should reject the 4th stream, got %d", r.Code)
	}
	close(release)
	time.Sleep(20 * time.Millisecond)
	if !l.acquire("1.1.1.1") {
		t.Fatal("slot not released after the handler returned")
	}
}
