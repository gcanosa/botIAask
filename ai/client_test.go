package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStripThink(t *testing.T) {
	for _, c := range []struct {
		in, want string
		ok       bool
	}{
		{"Paris", "Paris", true},
		{"<think>user asks capital…</think>\n\nParis", "Paris", true},
		{"reasoning opened by the template</think> Paris", "Paris", true},
		{"<think></think>\nParis", "Paris", true},
		{"<think>still going", "", false},
	} {
		got, ok := stripThink(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("stripThink(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models") {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"object":"list","data":[{"id":"qwen3-4b-instruct-2507"},{"id":"text-embedding-nomic-embed-text-v1.5"},{"id":"meta-llama-3.1-8b-instruct"}]}`))
	}))
	defer srv.Close()
	got, err := NewModelLister(srv.URL + "/v1").ListModels(context.Background())
	if err != nil || strings.Join(got, ",") != "meta-llama-3.1-8b-instruct,qwen3-4b-instruct-2507" {
		t.Fatalf("got %v, %v", got, err)
	}
}
