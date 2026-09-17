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

func TestFetchPullRequest_200(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"number": 42, "title": "Fix bug", "state": "open", "merged": false, "html_url": "https://x/pull/42", "user": {"login": "bob"}}`))
	})
	info, err := FetchPullRequest(context.Background(), "owner", "repo", "", 42)
	if err != nil {
		t.Fatalf("FetchPullRequest: %v", err)
	}
	if info == nil || info.Number != 42 || info.Title != "Fix bug" || info.Author != "bob" {
		t.Fatalf("unexpected PR info: %+v", info)
	}
}

func TestFetchPullRequest_404ReturnsNilNilNotError(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	info, err := FetchPullRequest(context.Background(), "owner", "repo", "", 999)
	if err != nil {
		t.Fatalf("expected no error on 404, got %v", err)
	}
	if info != nil {
		t.Fatalf("expected nil info on 404, got %+v", info)
	}
}

func TestFetchPullRequest_UsesBearerAuthWhenTokenSet(t *testing.T) {
	var gotAuth string
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"number": 1}`))
	})
	if _, err := FetchPullRequest(context.Background(), "owner", "repo", "sekrit", 1); err != nil {
		t.Fatalf("FetchPullRequest: %v", err)
	}
	if gotAuth != "Bearer sekrit" {
		t.Fatalf("expected Bearer auth header, got %q", gotAuth)
	}
}

func TestFetchIssue_200(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"number": 45, "title": "Something broke", "state": "open", "html_url": "https://x/issues/45", "user": {"login": "dave"}}`))
	})
	info, err := FetchIssue(context.Background(), "owner", "repo", "", 45)
	if err != nil {
		t.Fatalf("FetchIssue: %v", err)
	}
	if info == nil || info.Number != 45 || info.Author != "dave" {
		t.Fatalf("unexpected issue info: %+v", info)
	}
}

func TestFetchIssue_404ReturnsNilNil(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	info, err := FetchIssue(context.Background(), "owner", "repo", "", 999)
	if err != nil || info != nil {
		t.Fatalf("expected nil, nil on 404, got %+v, %v", info, err)
	}
}

func TestFetchCommit_200(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"sha": "abcd1234", "html_url": "https://x/commit/abcd1234", "commit": {"message": "fix: thing\n\nbody"}, "author": {"login": "alice"}}`))
	})
	info, err := FetchCommit(context.Background(), "owner", "repo", "", "abcd1234")
	if err != nil {
		t.Fatalf("FetchCommit: %v", err)
	}
	if info == nil || info.SHA != "abcd1234" || info.Message != "fix: thing" || info.Author != "alice" {
		t.Fatalf("unexpected commit info: %+v", info)
	}
}

func TestFetchCommit_404ReturnsNilNil(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	info, err := FetchCommit(context.Background(), "owner", "repo", "", "deadbeef")
	if err != nil || info != nil {
		t.Fatalf("expected nil, nil on 404, got %+v, %v", info, err)
	}
}
