package irc

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"botIAask/config"
)

func TestTimeCommandListsEveryZone(t *testing.T) {
	n, out := newCaptureNetwork(t, &config.Config{})
	n.handleTimeCommand("#c")
	got := strings.Join(out.all(), " ")
	for _, city := range []string{"Honolulu", "Barcelona, Spain", "Auckland"} {
		if !strings.Contains(got, city) {
			t.Errorf("%s missing from %q", city, got)
		}
	}
	if len(worldTimeZones()) != len(worldTimeCities) {
		t.Errorf("zones loaded: %d of %d", len(worldTimeZones()), len(worldTimeCities))
	}
}

// A city carries its zone; a country falls back to the forecast endpoint's timezone=auto.
func TestTimePlaceCommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/forecast":
			w.Write([]byte(`{"timezone":"Europe/Madrid"}`))
		case r.URL.Query().Get("name") == "barcelona":
			w.Write([]byte(`{"results":[{"name":"Barcelona","admin1":"Catalonia","country":"Spain","timezone":"Europe/Madrid"}]}`))
		case r.URL.Query().Get("name") == "Spain":
			w.Write([]byte(`{"results":[{"name":"Spain","country":"Spain","latitude":40,"longitude":-4}]}`))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	oldGeo, oldTZ := timeGeoAPI, timeTZAPI
	timeGeoAPI, timeTZAPI = srv.URL, srv.URL
	defer func() { timeGeoAPI, timeTZAPI = oldGeo, oldTZ }()

	n, out := newCmdNetwork(t)
	n.handleTimePlaceCommand("#c", "alice", "barcelona")
	n.handleTimePlaceCommand("#c", "alice", "Spain")
	n.handleTimePlaceCommand("#c", "alice", "nowhere")
	got := out.all()
	if len(got) != 3 {
		t.Fatalf("got %d lines: %q", len(got), got)
	}
	for i, want := range []string{"\x02Barcelona, Catalonia, Spain\x02 · ", "\x02Spain\x02 · ", `no place found for "nowhere"`} {
		if !strings.Contains(got[i], want) {
			t.Errorf("line %d: %q missing %q", i, got[i], want)
		}
	}
	if !strings.Contains(got[1], ircLightCyan+"Europe/Madrid (UTC+0") {
		t.Errorf("zone missing: %q", got[1])
	}
}
