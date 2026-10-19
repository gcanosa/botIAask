package irc

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Picks the requested language's entry, strips HTML/example lists, retries lower-cased on 404.
func TestDictCommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/casa") {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"en":[{"partOfSpeech":"Noun","language":"English","definitions":[{"definition":"slang house"}]}],
			"es":[{"partOfSpeech":"Noun","language":"Spanish","definitions":[
				{"definition":"<a href=\"/wiki/house\">house</a>, home<ul><li>example</li></ul>"},
				{"definition":"<span></span>"},
				{"definition":"family &amp; household"}]}]}`))
	}))
	defer srv.Close()
	old := dictAPI
	dictAPI = srv.URL
	defer func() { dictAPI = old }()

	n, out := newCmdNetwork(t)
	n.handleDictCommand("#c", "alice", "es:Casa")
	n.handleDictCommand("#c", "alice", "it:casa")
	got := out.all()
	if len(got) != 2 {
		t.Fatalf("got %d lines: %q", len(got), got)
	}
	if w := "\x02casa\x02 (Spanish) · \x02Noun\x02: house, home; family & household · " + srv.URL + "/wiki/casa#Spanish"; !strings.Contains(got[0], w) {
		t.Errorf("missing %q in %q", w, got[0])
	}
	if !strings.Contains(got[1], `no it definition for "casa"`) {
		t.Errorf("got %q", got[1])
	}
}
