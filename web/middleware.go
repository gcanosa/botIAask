package web

import (
	"net/http"
	"sync"
)

// maxBodyBytes caps request bodies on every route except /upload (multipart file uploads
// enforce their own, much larger, limit in handleUploadFile).
const maxBodyBytes = 1 << 20

// secure wraps the mux with baseline response headers and a request-body cap.
// ponytail: no CSP yet — the dashboard relies on inline onclick handlers; add one after
// migrating them to addEventListener.
func secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		if r.Body != nil && r.URL.Path != "/upload" {
			r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// connLimiter caps concurrent long-lived responses (SSE streams): each costs a goroutine, a ticker
// and often an open file, and the routes are reachable without logging in.
type connLimiter struct {
	mu       sync.Mutex
	perIP    map[string]int
	total    int
	maxPerIP int
	maxTotal int
}

func newConnLimiter(maxPerIP, maxTotal int) *connLimiter {
	return &connLimiter{perIP: make(map[string]int), maxPerIP: maxPerIP, maxTotal: maxTotal}
}

func (l *connLimiter) acquire(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.total >= l.maxTotal || l.perIP[ip] >= l.maxPerIP {
		return false
	}
	l.total++
	l.perIP[ip]++
	return true
}

func (l *connLimiter) release(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.total--
	if l.perIP[ip]--; l.perIP[ip] <= 0 {
		delete(l.perIP, ip)
	}
}

// wrap answers 429 once the caller's IP (or the server) is at its limit.
func (l *connLimiter) wrap(next http.HandlerFunc, ipOf func(*http.Request) string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := ipOf(r)
		if !l.acquire(ip) {
			http.Error(w, "too many open streams", http.StatusTooManyRequests)
			return
		}
		defer l.release(ip)
		next(w, r)
	}
}
