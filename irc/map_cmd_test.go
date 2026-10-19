package irc

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Found place → centered link zoomed by place_rank; no match → error; 1 req/s budget spent → plain search link.
func TestMapCommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") == "bucharest" {
			w.Write([]byte(`[{"display_name":"Bucharest, Romania","lat":"44.43","lon":"26.10","place_rank":8,"addresstype":"city"}]`))
			return
		}
		if r.URL.Query().Get("q") == "italy" {
			w.Write([]byte(`[{"display_name":"Italy","lat":"42.63","lon":"12.67","place_rank":4}]`))
			return
		}
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	old := mapAPI
	mapAPI = srv.URL
	defer func() { mapAPI = old }()

	n, out := newCmdNetwork(t)
	mapLast = time.Time{}
	n.handleMapCommand("#c", "alice", "italy")
	n.handleMapCommand("#c", "alice", "nowhere land") // within 1s: no lookup
	mapLast = time.Time{}
	n.handleMapCommand("#c", "alice", "bucharest")
	mapLast = time.Time{}
	n.handleMapCommand("#c", "alice", "zzqx")
	got := out.all()
	want := []string{
		"\x02Italy\x02 (42.63, 12.67) · https://www.google.com/maps/@?api=1&map_action=map&center=42.63,12.67&zoom=5",
		"\x02nowhere land\x02 · https://www.google.com/maps/search/?api=1&query=nowhere+land",
		"center=44.43,26.10&zoom=12", // capital: rank 8, but addresstype city wins
		`no place found for "zzqx"`,
	}
	if len(got) != 4 {
		t.Fatalf("got %d lines: %q", len(got), got)
	}
	for i, w := range want {
		if !strings.Contains(got[i], w) {
			t.Errorf("line %d: missing %q in %q", i, w, got[i])
		}
	}
}
