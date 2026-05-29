package web

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// LoginRateLimiter tracks login attempts per IP to prevent brute force attacks.
type LoginRateLimiter struct {
	mu      sync.Mutex
	attempts map[string][]time.Time
	window  time.Duration
	maxAttempts int
}

// NewLoginRateLimiter creates a new rate limiter with max attempts per window.
func NewLoginRateLimiter(window time.Duration, maxAttempts int) *LoginRateLimiter {
	l := &LoginRateLimiter{
		attempts: make(map[string][]time.Time),
		window:   window,
		maxAttempts: maxAttempts,
	}
	go l.cleanupLoop()
	return l
}

// IsAllowed checks if the IP is allowed to attempt a login. Returns false if rate limited.
func (l *LoginRateLimiter) IsAllowed(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-l.window)

	if times, exists := l.attempts[ip]; exists {
		// Remove old attempts outside the window
		var valid []time.Time
		for _, t := range times {
			if t.After(cutoff) {
				valid = append(valid, t)
			}
		}
		l.attempts[ip] = valid

		// Check if limit exceeded
		if len(valid) >= l.maxAttempts {
			return false
		}
	}

	// Record this attempt
	l.attempts[ip] = append(l.attempts[ip], now)
	return true
}

// cleanupLoop periodically removes old IP entries to prevent memory leaks.
func (l *LoginRateLimiter) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		l.mu.Lock()
		now := time.Now()
		cutoff := now.Add(-l.window * 2) // Keep entries older than 2x window

		for ip, times := range l.attempts {
			var valid []time.Time
			for _, t := range times {
				if t.After(cutoff) {
					valid = append(valid, t)
				}
			}
			if len(valid) == 0 {
				delete(l.attempts, ip)
			} else {
				l.attempts[ip] = valid
			}
		}
		l.mu.Unlock()
	}
}

// GetClientIP extracts the real client IP from a request.
// When trustForwarded is true, X-Forwarded-For and X-Real-IP headers are
// trusted (use only behind a known reverse proxy). Otherwise RemoteAddr is used.
func GetClientIP(r *http.Request, trustForwarded bool) string {
	if trustForwarded {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			ips := strings.Split(xff, ",")
			if len(ips) > 0 {
				if ip := strings.TrimSpace(ips[0]); ip != "" {
					return ip
				}
			}
		}
		if xri := r.Header.Get("X-Real-IP"); xri != "" {
			return xri
		}
	}

	// Fall back to RemoteAddr (strip port)
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}
