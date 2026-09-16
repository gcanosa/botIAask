package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func withTestServer(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	oldBase, oldClient := apiBase, eventsHTTPClient
	apiBase = srv.URL
	eventsHTTPClient = srv.Client()
	t.Cleanup(func() {
		apiBase = oldBase
		eventsHTTPClient = oldClient
	})
}

func TestFetchRepoEvents_SendsIfNoneMatch(t *testing.T) {
	var gotIfNoneMatch string
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotIfNoneMatch = r.Header.Get("If-None-Match")
		w.Write([]byte("[]"))
	})

	_, err := FetchRepoEvents(context.Background(), "owner", "repo", "", `"etag-value"`)
	if err != nil {
		t.Fatalf("FetchRepoEvents: %v", err)
	}
	if gotIfNoneMatch != `"etag-value"` {
		t.Fatalf("expected If-None-Match header sent, got %q", gotIfNoneMatch)
	}
}

func TestFetchRepoEvents_304ShortCircuits(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	})

	result, err := FetchRepoEvents(context.Background(), "owner", "repo", "", "")
	if err != nil {
		t.Fatalf("FetchRepoEvents: %v", err)
	}
	if !result.NotModified {
		t.Fatal("expected NotModified=true on 304")
	}
	if result.Events != nil {
		t.Fatal("expected no events decoded on 304")
	}
}

func TestFetchRepoEvents_ParsesRateLimitHeaders(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "42")
		w.Write([]byte("[]"))
	})

	result, err := FetchRepoEvents(context.Background(), "owner", "repo", "", "")
	if err != nil {
		t.Fatalf("FetchRepoEvents: %v", err)
	}
	if result.RateRemaining != 42 {
		t.Fatalf("expected RateRemaining=42, got %d", result.RateRemaining)
	}
}

func TestFetchRepoEvents_BearerAuthHeaderWhenTokenSet(t *testing.T) {
	var gotAuth string
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte("[]"))
	})

	if _, err := FetchRepoEvents(context.Background(), "owner", "repo", "", ""); err != nil {
		t.Fatalf("FetchRepoEvents: %v", err)
	}
	if gotAuth != "" {
		t.Fatalf("expected no Authorization header without a token, got %q", gotAuth)
	}

	if _, err := FetchRepoEvents(context.Background(), "owner", "repo", "sekrit", ""); err != nil {
		t.Fatalf("FetchRepoEvents: %v", err)
	}
	if gotAuth != "Bearer sekrit" {
		t.Fatalf("expected Bearer auth header, got %q", gotAuth)
	}
}
