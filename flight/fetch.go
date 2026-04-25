package flight

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const userAgent = "botIAask/flight (+https://github.com/)"

// FetchParams for AirLabs Data API (https://airlabs.co/docs/flight). API key is required.
// One !flight = 1× /flight + 2× /airports (dep/arr) for city/country/timezone. Paid tiers support high daily volume.
type FetchParams struct {
	BaseURL  string
	APIKey   string
	HTTP     *http.Client
	FlightID string
}

// Snapshot is a normalized leg + live position (AirLabs /flight + airport lookup).
type Snapshot struct {
	OK    bool
	Error string

	FlightIATA, FlightICAO, FlightNumber string
	AirlineIATA, AirlineName              string
	AirlineICAO                          string
	Status                               string
	Codeshare                            string
	DurationMin                          int

	Dep, Arr endpointLeg

	DepDelayMin, ArrDelayMin *int

	Aircraft  string
	Live      liveModel
	APIAttribution string
}

type endpointLeg struct {
	Airport   string
	IATA      string
	ICAO      string
	Timezone  string
	City      string
	Country   string
	Terminal  string
	Gate      string
	Baggage   string
	Delay     *int
	Scheduled *time.Time
	Estimated *time.Time
	Actual    *time.Time
	LocalStr  string
}

type liveModel struct {
	HasData bool
	Lat, Lon, AltM, SpeedKmh, Hdg *float64
	Reg, Squawk, Hex, Flag         string
}

// Fetch loads GET /flight + parallel GET /airports (dep+arr) when possible.
func Fetch(ctx context.Context, p FetchParams) (*Snapshot, error) {
	key := strings.TrimSpace(p.APIKey)
	if key == "" {
		return &Snapshot{OK: false, Error: "missing API key: set flight.api_key in config or AIRLABS_API_KEY (https://airlabs.co)"}, nil
	}
	fid := strings.ToUpper(strings.TrimSpace(p.FlightID))
	flightI := flightIATAString(fid)
	if flightI == "" {
		return &Snapshot{OK: false, Error: "invalid flight id (e.g. AA100, U2800)"}, nil
	}

	base := strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
	if base == "" {
		return &Snapshot{OK: false, Error: "empty base_url"}, nil
	}
	client := p.HTTP
	if client == nil {
		client = &http.Client{Timeout: 35 * time.Second}
	}
	raw, err := getJSON(ctx, client, base, "/flight", key, map[string]string{"flight_iata": flightI})
	if err != nil {
		return &Snapshot{OK: false, Error: err.Error()}, nil
	}
	if raw == nil {
		return &Snapshot{OK: false, Error: "no flight in response"}, nil
	}
	snap, perr := normalizeAirlabs(raw)
	if perr != nil {
		return &Snapshot{OK: false, Error: perr.Error()}, nil
	}
	depI, arrI := strings.TrimSpace(snap.Dep.IATA), strings.TrimSpace(snap.Arr.IATA)
	var depAP, arrAP *airportRow
	var wg sync.WaitGroup
	if depI != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			depAP, _ = fetchAirport(ctx, client, base, key, depI)
		}()
	}
	if arrI != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			arrAP, _ = fetchAirport(ctx, client, base, key, arrI)
		}()
	}
	wg.Wait()
	mergeAirport(snap, depAP, arrAP)
	snap.OK = true
	snap.APIAttribution = "airlabs.co"
	return snap, nil
}

func mergeAirport(s *Snapshot, dep, arr *airportRow) {
	if s == nil {
		return
	}
	if dep != nil {
		if s.Dep.City == "" {
			s.Dep.City = dep.City
		}
		if s.Dep.Country == "" && dep.CountryCode != "" {
			s.Dep.Country = countryName(dep.CountryCode)
		}
		if s.Dep.Airport == "" {
			s.Dep.Airport = dep.Name
		}
		if s.Dep.Timezone == "" {
			s.Dep.Timezone = dep.Timezone
		}
	}
	if arr != nil {
		if s.Arr.City == "" {
			s.Arr.City = arr.City
		}
		if s.Arr.Country == "" && arr.CountryCode != "" {
			s.Arr.Country = countryName(arr.CountryCode)
		}
		if s.Arr.Airport == "" {
			s.Arr.Airport = arr.Name
		}
		if s.Arr.Timezone == "" {
			s.Arr.Timezone = arr.Timezone
		}
	}
}

type airportRow struct {
	Name, City, CountryCode, Timezone, IATA, ICAO string
}

func fetchAirport(ctx context.Context, client *http.Client, base, key, iata string) (*airportRow, error) {
	if iata == "" {
		return nil, nil
	}
	raw, err := getJSON(ctx, client, base, "/airports", key, map[string]string{
		"iata_code": iata,
		"_fields":   "name,city,country_code,timezone,iata_code,icao_code",
	})
	if err != nil {
		return nil, err
	}
	var one struct {
		Name        string `json:"name"`
		City        string `json:"city"`
		CountryCode string `json:"country_code"`
		Timezone    string `json:"timezone"`
		IataCode    string `json:"iata_code"`
		IcaoCode    string `json:"icao_code"`
	}
	switch v := raw.(type) {
	case []interface{}:
		if len(v) == 0 {
			return nil, nil
		}
		ob, _ := json.Marshal(v[0])
		_ = json.Unmarshal(ob, &one)
	case map[string]interface{}:
		ob, _ := json.Marshal(v)
		_ = json.Unmarshal(ob, &one)
	default:
		return nil, nil
	}
	if one.Name == "" && one.City == "" {
		return nil, nil
	}
	return &airportRow{
		Name: one.Name, City: one.City, CountryCode: one.CountryCode, Timezone: one.Timezone,
		IATA: one.IataCode, ICAO: one.IcaoCode,
	}, nil
}

// getJSON requests AirLabs: unwrap {response: ...} or top-level, returns error as body message.
func getJSON(ctx context.Context, client *http.Client, base, path, key string, q map[string]string) (interface{}, error) {
	u, err := url.Parse(base + path)
	if err != nil {
		return nil, err
	}
	qs := u.Query()
	qs.Set("api_key", key)
	for k, v := range q {
		if v != "" {
			qs.Set(k, v)
		}
	}
	u.RawQuery = qs.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		clip := min(200, len(body))
		return nil, fmt.Errorf("AirLabs HTTP %d: %s", res.StatusCode, string(body)[:clip])
	}
	var any interface{}
	if err := json.Unmarshal(body, &any); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if m, ok := any.(map[string]interface{}); ok {
		if errBox, ok := m["error"]; ok && errBox != nil {
			eb, _ := errBox.(map[string]interface{})
			if msg, _ := eb["message"].(string); msg != "" {
				return nil, fmt.Errorf("AirLabs: %s", msg)
			}
			return nil, fmt.Errorf("AirLabs error: %#v", errBox)
		}
		if r, o := m["response"]; o {
			return r, nil
		}
	}
	return any, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

type rawAirlabs struct {
	FlightIATA  string  `json:"flight_iata"`
	FlightICAO  string  `json:"flight_icao"`
	FlightNumber string `json:"flight_number"`
	AirlineIATA string  `json:"airline_iata"`
	AirlineICAO string  `json:"airline_icao"`
	DepIATA     string  `json:"dep_iata"`
	DepICAO     string  `json:"dep_icao"`
	DepTerminal string  `json:"dep_terminal"`
	DepGate     string  `json:"dep_gate"`
	DepTime     string  `json:"dep_time"`
	DepTimeUTC  string  `json:"dep_time_utc"`
	DepTimeTS   *float64 `json:"dep_time_ts"`
	DepEstUTC   string  `json:"dep_estimated_utc"`
	DepEstTS    *float64 `json:"dep_estimated_ts"`
	DepActUTC   string  `json:"dep_actual_utc"`
	DepActTS    *float64 `json:"dep_actual_ts"`
	DepDelayed  *float64 `json:"dep_delayed"`
	ArrIATA     string  `json:"arr_iata"`
	ArrICAO     string  `json:"arr_icao"`
	ArrTerminal string  `json:"arr_terminal"`
	ArrGate     string  `json:"arr_gate"`
	ArrBaggage  string  `json:"arr_baggage"`
	ArrTime     string  `json:"arr_time"`
	ArrTimeUTC  string  `json:"arr_time_utc"`
	ArrTimeTS   *float64 `json:"arr_time_ts"`
	ArrEstUTC   string  `json:"arr_estimated_utc"`
	ArrEstTS    *float64 `json:"arr_estimated_ts"`
	ArrActUTC   string  `json:"arr_actual_utc"`
	ArrActTS    *float64 `json:"arr_actual_ts"`
	ArrDelayed  *float64 `json:"arr_delayed"`
	CsAirlineIATA *string `json:"cs_airline_iata"`
	CsFlightNum   *string `json:"cs_flight_number"`
	CsFlightIATA  *string `json:"cs_flight_iata"`
	Duration      *float64 `json:"duration"`
	Status        string  `json:"status"`
	Hex           string  `json:"hex"`
	RegNumber     string  `json:"reg_number"`
	Manufacturer  string  `json:"manufacturer"`
	Model         string  `json:"model"`
	Flag     string   `json:"flag"`
	Lat      *float64 `json:"lat"`
	Lng      *float64 `json:"lng"`
	Alt      *float64 `json:"alt"`
	Dir      *float64 `json:"dir"`
	Speed    *float64 `json:"speed"`
	Squawk   string   `json:"squawk"`
}

func normalizeAirlabs(v interface{}) (*Snapshot, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var raw rawAirlabs
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	if strings.TrimSpace(raw.FlightIATA) == "" {
		return nil, fmt.Errorf("empty flight data")
	}
	s := &Snapshot{
		FlightIATA: raw.FlightIATA, FlightICAO: raw.FlightICAO, FlightNumber: raw.FlightNumber,
		AirlineIATA: raw.AirlineIATA, AirlineICAO: raw.AirlineICAO,
		Status: mapAirlabsStatus(raw.Status),
	}
	if raw.Duration != nil {
		s.DurationMin = int(*raw.Duration + 0.5)
	}
	// Delays: prefer explicit minutes fields
	if raw.DepDelayed != nil {
		d := int(*raw.DepDelayed + 0.5)
		s.DepDelayMin = &d
	}
	if raw.ArrDelayed != nil {
		d := int(*raw.ArrDelayed + 0.5)
		s.ArrDelayMin = &d
	}
	// map onto endpoint delay *int
	s.Dep = endpointLeg{IATA: raw.DepIATA, ICAO: raw.DepICAO, Terminal: raw.DepTerminal, Gate: raw.DepGate, LocalStr: raw.DepTime}
	s.Arr = endpointLeg{IATA: raw.ArrIATA, ICAO: raw.ArrICAO, Terminal: raw.ArrTerminal, Gate: raw.ArrGate, Baggage: raw.ArrBaggage, LocalStr: raw.ArrTime}
	if s.DepDelayMin != nil {
		d := *s.DepDelayMin
		s.Dep.Delay = &d
	}
	if s.ArrDelayMin != nil {
		d := *s.ArrDelayMin
		s.Arr.Delay = &d
	}
	// times — prefer ts for block math, fall back to UTC string
	s.Dep.Scheduled = tsPtr(raw.DepTimeTS)
	if s.Dep.Scheduled == nil {
		s.Dep.Scheduled = parseAirlabsUTCTime(raw.DepTimeUTC)
	}
	s.Dep.Estimated = firstNonNil(tsPtr(raw.DepEstTS), parseAirlabsUTCTime(raw.DepEstUTC))
	s.Dep.Actual = firstNonNil(tsPtr(raw.DepActTS), parseAirlabsUTCTime(raw.DepActUTC))
	s.Arr.Scheduled = tsPtr(raw.ArrTimeTS)
	if s.Arr.Scheduled == nil {
		s.Arr.Scheduled = parseAirlabsUTCTime(raw.ArrTimeUTC)
	}
	s.Arr.Estimated = firstNonNil(tsPtr(raw.ArrEstTS), parseAirlabsUTCTime(raw.ArrEstUTC))
	s.Arr.Actual = firstNonNil(tsPtr(raw.ArrActTS), parseAirlabsUTCTime(raw.ArrActUTC))
	if raw.CsFlightIATA != nil && *raw.CsFlightIATA != "" {
		s.Codeshare = "codeshare: " + *raw.CsFlightIATA
	} else if raw.CsAirlineIATA != nil && raw.CsFlightNum != nil {
		s.Codeshare = "codeshare: " + *raw.CsAirlineIATA + *raw.CsFlightNum
	}
	// aircraft
	if raw.Model != "" || raw.Manufacturer != "" || raw.RegNumber != "" {
		s.Aircraft = strings.TrimSpace(raw.Manufacturer + " " + raw.Model + " · " + raw.RegNumber)
		s.Aircraft = strings.ReplaceAll(s.Aircraft, "  ", " ")
	}
	// live
	if raw.Alt != nil || raw.Speed != nil {
		s.Live.HasData = true
		s.Live.AltM = raw.Alt
		if raw.Speed != nil {
			spd := *raw.Speed
			s.Live.SpeedKmh = &spd
		}
		s.Live.Lat, s.Live.Lon = raw.Lat, raw.Lng
		s.Live.Hdg = raw.Dir
		s.Live.Hex = raw.Hex
		s.Live.Squawk = raw.Squawk
		s.Live.Reg = raw.RegNumber
		s.Live.Flag = raw.Flag
	}
	return s, nil
}

func mapAirlabsStatus(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "en-route":
		return "active"
	default:
		return s
	}
}

func tsPtr(f *float64) *time.Time {
	if f == nil {
		return nil
	}
	t := time.Unix(int64(*f), 0).UTC()
	return &t
}
func firstNonNil(a, b *time.Time) *time.Time {
	if a != nil {
		return a
	}
	return b
}
func parseAirlabsUTCTime(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05", "2006-01-02 15:04", time.RFC3339,
	} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return &t
		}
	}
	return nil
}

// validFlightIATA for tests
func validFlightIATA(s string) bool { return flightIATAString(s) != "" }
