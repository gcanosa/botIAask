package flight

import (
	"encoding/json"
	"testing"
	"time"
)

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
