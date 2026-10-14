package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStaticAssetsRevalidateWithETag(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/static/app.js", nil)
	rec := httptest.NewRecorder()
	s.handleStatic(rec, req)
	if rec.Code != http.StatusOK || rec.Body.Len() < 1000 {
		t.Fatalf("first load: %d, %d bytes", rec.Code, rec.Body.Len())
	}
	etag := rec.Header().Get("ETag")
	if etag == "" || rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("missing validators: etag=%q cc=%q", etag, rec.Header().Get("Cache-Control"))
	}

	req = httptest.NewRequest(http.MethodGet, "/static/app.js", nil)
	req.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	s.handleStatic(rec, req)
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Fatalf("revalidation: %d with %d body bytes", rec.Code, rec.Body.Len())
	}

	rec = httptest.NewRecorder()
	s.handleStatic(rec, httptest.NewRequest(http.MethodGet, "/static/secret.txt", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown asset: %d", rec.Code)
	}
}
