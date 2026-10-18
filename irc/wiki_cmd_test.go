package irc

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Rank by "index", not response order; disambiguation pages drop out of "also:" and turn
// an ambiguous top hit into a candidate list.
func TestWikiCommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/es/") && r.URL.Query().Get("gsrsearch") == "mercury" {
			w.Write([]byte(`{"query":{"pages":[
				{"title":"Freddie Mercury","index":2},
				{"title":"Mercury","index":1,"pageprops":{"disambiguation":""}},
				{"title":"Mercury (planet)","index":3}]}}`))
			return
		}
		w.Write([]byte(`{"query":{"pages":[
			{"title":"Argentina national football team","index":2},
			{"title":"Argentina","index":1,"description":"Country in South America"},
			{"title":"Argentina (disambiguation)","index":3,"pageprops":{"disambiguation":""}}]}}`))
	}))
	defer srv.Close()
	old := wikiAPI
	wikiAPI = srv.URL + "/%s"
	defer func() { wikiAPI = old }()

	n, out := newCmdNetwork(t)
	n.handleWikiCommand("#c", "alice", "es:Argentina")
	n.handleWikiCommand("#c", "alice", "mercury")
	got := out.all()
	want := []string{
		"\x02Argentina\x02 — Country in South America · " + srv.URL + "/es/wiki/Argentina · also: Argentina national football team",
		"\x02Mercury\x02 is ambiguous: Freddie Mercury, Mercury (planet) · " + srv.URL + "/en/wiki/Mercury",
	}
	if len(got) != 2 {
		t.Fatalf("got %d lines: %q", len(got), got)
	}
	for i, w := range want {
		if !strings.Contains(got[i], w) {
			t.Errorf("line %d: missing %q in %q", i, w, got[i])
		}
	}
}
