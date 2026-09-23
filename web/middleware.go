package web

import (
	"net/http"
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
