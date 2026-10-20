package irc

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// Nominatim wins when it answers ("Texas" is a state, not Open-Meteo's fuzzy town hit); otherwise
// Open-Meteo: a city carries its zone, a country falls back to the forecast endpoint's timezone=auto.
func TestTimePlaceCommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/forecast" && r.URL.Query().Get("latitude") == "31.26":
			w.Write([]byte(`{"timezone":"America/Chicago"}`))
		case r.URL.Path == "/v1/forecast":
			w.Write([]byte(`{"timezone":"Europe/Madrid"}`))
		case r.URL.Query().Get("name") == "barcelona":
			w.Write([]byte(`{"results":[{"name":"Barcelona","admin1":"Catalonia","country":"Spain","timezone":"Europe/Madrid"}]}`))
		case r.URL.Path == "/search" && r.URL.Query().Get("q") == "Texas":
			w.Write([]byte(`[{"display_name":"Texas, United States","lat":"31.26","lon":"-98.54"}]`))
		case r.URL.Path == "/search":
			w.Write([]byte(`[]`))
		case r.URL.Query().Get("name") == "Texas": // Open-Meteo has no states: fuzzy town hit
			w.Write([]byte(`{"results":[{"name":"Colfax","admin1":"West Virginia","country":"United States","timezone":"America/New_York"}]}`))
		case r.URL.Query().Get("name") == "Spain":
			w.Write([]byte(`{"results":[{"name":"Spain","country":"Spain","latitude":40,"longitude":-4}]}`))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	oldGeo, oldTZ, oldMap := timeGeoAPI, timeTZAPI, mapAPI
	timeGeoAPI, timeTZAPI, mapAPI = srv.URL, srv.URL, srv.URL
	defer func() { timeGeoAPI, timeTZAPI, mapAPI = oldGeo, oldTZ, oldMap }()

	n, out := newCmdNetwork(t)
	n.handleTimePlaceCommand("#c", "alice", "barcelona")
	n.handleTimePlaceCommand("#c", "alice", "Spain")
	mapLast = time.Time{} // Nominatim 1 req/s budget
	n.handleTimePlaceCommand("#c", "alice", "nowhere")
	mapLast = time.Time{}
	n.handleTimePlaceCommand("#c", "alice", "Texas")
	got := out.all()
	if len(got) != 4 {
		t.Fatalf("got %d lines: %q", len(got), got)
	}
	for i, want := range []string{"\x02Barcelona, Catalonia, Spain\x02 · ", "\x02Spain\x02 · ", `no place found for "nowhere"`, "\x02Texas, United States\x02 · "} {
		if !strings.Contains(got[i], want) {
			t.Errorf("line %d: %q missing %q", i, got[i], want)
		}
	}
	if !strings.Contains(got[1], ircLightCyan+"Europe/Madrid (UTC+0") {
		t.Errorf("zone missing: %q", got[1])
	}
	if !strings.Contains(got[3], ircLightCyan+"America/Chicago (UTC-0") {
		t.Errorf("zone missing: %q", got[3])
	}
}
