package flight

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// sampleAirlabsSchedulesJSON mimics AirLabs GET /schedules?flight_iata=AA1004,
// which returns an array of entries (one per scheduled day) instead of the
// single object /flight returns for the currently in-progress leg.
const sampleAirlabsSchedulesJSON = `{"response": [
  {
    "flight_iata": "AA1004",
    "airline_iata": "AA",
    "dep_iata": "SFO",
    "arr_iata": "DFW",
    "dep_time": "2026-07-10 04:20",
    "arr_time": "2026-07-10 12:00",
    "status": "scheduled"
  },
  {
    "flight_iata": "AA1004",
    "airline_iata": "AA",
    "dep_iata": "SFO",
    "arr_iata": "DFW",
    "dep_time": "2026-07-18 04:20",
    "arr_time": "2026-07-18 12:00",
    "status": "scheduled"
  }
]}`

const sampleAirlabsFlightJSON = `{
  "flight_iata": "AA1004",
  "airline_iata": "AA",
  "dep_iata": "SFO",
  "arr_iata": "DFW",
  "dep_time": "2019-12-12 04:20",
  "dep_time_ts": 1576143600,
  "arr_time_ts": 1576166400,
  "arr_time": "2019-12-12 12:00",
  "dep_delayed": 13,
  "duration": 240,
  "status": "en-route",
  "manufacturer": "AIRBUS",
  "model": "A321",
  "reg_number": "N12345",
  "lat": 35.0,
  "lng": -105.0,
  "alt": 10000,
  "speed": 800,
  "dir": 90
}`

func TestParseFlightIATAString(t *testing.T) {
	if g := flightIATAString("AA100"); g != "AA100" {
		t.Fatalf("got %q", g)
	}
}

func TestNormalizeAirlabs(t *testing.T) {
	var m map[string]interface{}
	_ = json.Unmarshal([]byte(sampleAirlabsFlightJSON), &m)
	s, err := normalizeAirlabs(m)
	if err != nil {
		t.Fatal(err)
	}
	if s.FlightIATA != "AA1004" || s.Status != "active" {
		t.Fatalf("bad snap: status=%q iata=%q", s.Status, s.FlightIATA)
	}
	if s.Dep.IATA != "SFO" || s.Arr.IATA != "DFW" {
		t.Fatalf("legs")
	}
}

func TestFormatIRCLines(t *testing.T) {
	var m map[string]interface{}
	_ = json.Unmarshal([]byte(sampleAirlabsFlightJSON), &m)
	s, _ := normalizeAirlabs(m)
	s.OK = true
	s.APIAttribution = "airlabs.co"
	s.Dep.City, s.Arr.City = "San Francisco", "Dallas"
	s.Dep.Country, s.Arr.Country = "United States", "United States"
	s.Dep.Timezone = "America/Los_Angeles"
	s.Arr.Timezone = "America/Chicago"
	lines := FormatIRCLines(s, time.Unix(1576150000, 0).UTC())
	if len(lines) < 3 {
		t.Fatalf("lines %d", len(lines))
	}
}

func TestValidFlightIATA(t *testing.T) {
	if !validFlightIATA("AA100") {
		t.Fatal("AA100")
	}
}

func TestFetchScheduledFlight_MatchesDate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/schedules" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(sampleAirlabsSchedulesJSON))
	}))
	defer srv.Close()

	date := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	raw, err := fetchScheduledFlight(context.Background(), srv.Client(), srv.URL, "key", "AA1004", date)
	if err != nil {
		t.Fatalf("fetchScheduledFlight: %v", err)
	}
	m, ok := raw.(map[string]interface{})
	if !ok {
		t.Fatalf("expected a matched entry, got %#v", raw)
	}
	if depTime, _ := m["dep_time"].(string); depTime != "2026-07-18 04:20" {
		t.Fatalf("matched wrong entry: dep_time=%q", depTime)
	}
}

func TestFetchScheduledFlight_NoMatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(sampleAirlabsSchedulesJSON))
	}))
	defer srv.Close()

	date := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	raw, err := fetchScheduledFlight(context.Background(), srv.Client(), srv.URL, "key", "AA1004", date)
	if err != nil {
		t.Fatalf("fetchScheduledFlight: %v", err)
	}
	if raw != nil {
		t.Fatalf("expected no match, got %#v", raw)
	}
}

func TestFetch_WithDate_ReturnsScheduledSnapshot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/schedules":
			w.Write([]byte(sampleAirlabsSchedulesJSON))
		case "/airports":
			w.Write([]byte(`{"response": {"name": "Test Airport", "city": "Test City", "country_code": "US", "timezone": "UTC"}}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	date := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	snap, err := Fetch(context.Background(), FetchParams{
		BaseURL:  srv.URL,
		APIKey:   "key",
		FlightID: "AA1004",
		HTTP:     srv.Client(),
		Date:     &date,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !snap.OK {
		t.Fatalf("expected OK snapshot, got error: %s", snap.Error)
	}
	if snap.Dep.LocalStr != "2026-07-18 04:20" {
		t.Fatalf("expected the matched (2026-07-18) entry, got dep=%q", snap.Dep.LocalStr)
	}
	if snap.Live.HasData {
		t.Fatal("scheduled lookups should not have live position data")
	}
}

func TestFetch_WithDate_NoMatchReturnsCleanError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(sampleAirlabsSchedulesJSON))
	}))
	defer srv.Close()

	date := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	snap, err := Fetch(context.Background(), FetchParams{
		BaseURL:  srv.URL,
		APIKey:   "key",
		FlightID: "AA1004",
		HTTP:     srv.Client(),
		Date:     &date,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if snap.OK {
		t.Fatal("expected a not-OK snapshot for a date with no schedule match")
	}
	if snap.Error == "" {
		t.Fatal("expected a non-empty error message")
	}
}
