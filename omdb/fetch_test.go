package omdb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchByTitle_success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apikey") != "k" || r.URL.Query().Get("t") != "Test" || r.URL.Query().Get("plot") != "short" {
			t.Fatalf("query: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Response":"True","Title":"Test Film","Year":"2020","imdbRating":"7.5","Plot":"Short plot here."}`))
	}))
	defer ts.Close()

	ctx := context.Background()
	res, err := FetchByTitle(ctx, ts.Client(), "k", ts.URL+"/", "Test")
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Title != "Test Film" || res.Year != "2020" || res.ImdbRating != "7.5" || res.Plot != "Short plot here." {
		t.Fatalf("%+v", res)
	}
}

func TestFetchByTitle_notFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"Response":"False","Error":"Movie not found!"}`))
	}))
	defer ts.Close()

	res, err := FetchByTitle(context.Background(), ts.Client(), "k", ts.URL+"/", "Nope")
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || !strings.Contains(res.Error, "not found") {
		t.Fatalf("%+v", res)
	}
}

func TestFetchByTitle_emptyKey(t *testing.T) {
	_, err := FetchByTitle(context.Background(), http.DefaultClient, "", "http://x/", "t")
	if err == nil {
		t.Fatal("expected error")
	}
}
