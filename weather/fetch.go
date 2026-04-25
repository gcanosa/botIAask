package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const userAgent = "botIAask/weather (+https://github.com/)"

// LocationLabel is a resolved place name for display.
type LocationLabel struct {
	Display   string
	Latitude  float64
	Longitude float64
}

// Current is right-now conditions.
type Current struct {
	TempC      float64 `json:"temp_c"`
	ApparentC  float64 `json:"apparent_c"`
	Code       int     `json:"code"`
	Summary    string  `json:"summary"`
	Icon       string  `json:"icon"`
	WindKmh    float64 `json:"wind_kmh"`
	Humidity   int     `json:"humidity"`
	IsDay      int     `json:"is_day"`
}

// Day is one calendar row in the daily forecast.
type Day struct {
	Date     string  `json:"date"`
	Weekday  string  `json:"weekday"`
	Code     int     `json:"code"`
	Summary  string  `json:"summary"`
	Icon     string  `json:"icon"`
	MaxC     float64 `json:"max_c"`
	MinC     float64 `json:"min_c"`
	PoP      int     `json:"pop"`
}

// Snapshot is the API payload for the web panel.
type Snapshot struct {
	OK         bool     `json:"ok"`
	Message    string   `json:"message,omitempty"`
	Location   string   `json:"location,omitempty"`
	Timezone   string   `json:"timezone,omitempty"`
	FetchedAt  string   `json:"fetched_at,omitempty"`
	Current    *Current `json:"current,omitempty"`
	Daily      []Day    `json:"daily,omitempty"`
	Attribution string `json:"attribution"`
}

// FetchSnapshot resolves query (e.g. "Barcelona, Spain") and loads forecast from Open-Meteo.
func FetchSnapshot(ctx context.Context, client *http.Client, query string) (*Snapshot, error) {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	q := strings.TrimSpace(query)
	if q == "" {
		return &Snapshot{OK: false, Message: "location is empty", Attribution: "open-meteo.com"}, nil
	}

	loc, err := geocode(ctx, client, q)
	if err != nil {
		return &Snapshot{OK: false, Message: err.Error(), Attribution: "open-meteo.com"}, nil
	}
	if loc == nil {
		return &Snapshot{OK: false, Message: "place not found", Attribution: "open-meteo.com"}, nil
	}

	snap, err := forecast(ctx, client, loc)
	if err != nil {
		return &Snapshot{OK: false, Message: err.Error(), Attribution: "open-meteo.com"}, nil
	}
	return snap, nil
}

type geoResp struct {
	Results []struct {
		Name      string  `json:"name"`
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
		Country   string  `json:"country"`
		Admin1    string  `json:"admin1"`
	} `json:"results"`
}

func geocode(ctx context.Context, client *http.Client, name string) (*LocationLabel, error) {
	u := "https://geocoding-api.open-meteo.com/v1/search?" + url.Values{
		"name":    {name},
		"count":   {"1"},
		"language": {"en"},
	}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return nil, fmt.Errorf("geocoding: %s: %s", res.Status, strings.TrimSpace(string(b)))
	}
	var g geoResp
	if err := json.NewDecoder(res.Body).Decode(&g); err != nil {
		return nil, err
	}
	if len(g.Results) == 0 {
		return nil, nil
	}
	r := g.Results[0]
	var display string
	if r.Country != "" {
		if r.Admin1 != "" && r.Admin1 != r.Name {
			display = r.Name + ", " + r.Admin1 + ", " + r.Country
		} else {
			display = r.Name + ", " + r.Country
		}
	} else {
		display = r.Name
	}
	return &LocationLabel{
		Display:   display,
		Latitude:  r.Latitude,
		Longitude: r.Longitude,
	}, nil
}

type fcResp struct {
	Timezone     string            `json:"timezone"`
	Current      fcCurrent         `json:"current"`
	CurrentUnits map[string]string `json:"current_units"`
	Daily        fcDaily           `json:"daily"`
	DailyUnits   map[string]string `json:"daily_units"`
}

type fcCurrent struct {
	Temperature2m       *float64 `json:"temperature_2m"`
	ApparentTemperature *float64 `json:"apparent_temperature"`
	RelativeHumidity2m  *int     `json:"relative_humidity_2m"`
	WeatherCode         *int     `json:"weather_code"`
	WindSpeed10m        *float64 `json:"wind_speed_10m"`
	IsDay               *int     `json:"is_day"`
}

type fcDaily struct {
	Time                    []string   `json:"time"`
	WeatherCode             []int      `json:"weather_code"`
	Temperature2mMax        []float64  `json:"temperature_2m_max"`
	Temperature2mMin        []float64  `json:"temperature_2m_min"`
	PrecipitationProbMax    []int      `json:"precipitation_probability_max"`
}

func forecast(ctx context.Context, client *http.Client, loc *LocationLabel) (*Snapshot, error) {
	v := url.Values{}
	v.Set("latitude", formatCoord(loc.Latitude))
	v.Set("longitude", formatCoord(loc.Longitude))
	v.Set("timezone", "auto")
	v.Set("wind_speed_unit", "kmh")
	v.Set("forecast_days", "5")
	v.Set("current", strings.Join([]string{
		"temperature_2m", "apparent_temperature", "relative_humidity_2m", "weather_code", "wind_speed_10m", "is_day",
	}, ","))
	v.Set("daily", strings.Join([]string{
		"weather_code", "temperature_2m_max", "temperature_2m_min", "precipitation_probability_max",
	}, ","))
	u := "https://api.open-meteo.com/v1/forecast?" + v.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return nil, fmt.Errorf("forecast: %s: %s", res.Status, strings.TrimSpace(string(b)))
	}
	var raw fcResp
	if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		return nil, err
	}
	locName := strings.TrimSpace(loc.Display)
	tzName := raw.Timezone
	locTZ, err := time.LoadLocation(tzName)
	if err != nil {
		locTZ = time.UTC
		tzName = "UTC"
	}

	var cur Current
	if raw.Current.Temperature2m != nil {
		cur.TempC = *raw.Current.Temperature2m
	}
	if raw.Current.ApparentTemperature != nil {
		cur.ApparentC = *raw.Current.ApparentTemperature
	} else {
		cur.ApparentC = cur.TempC
	}
	if raw.Current.RelativeHumidity2m != nil {
		cur.Humidity = *raw.Current.RelativeHumidity2m
	}
	if raw.Current.WeatherCode != nil {
		cur.Code = *raw.Current.WeatherCode
	}
	if raw.Current.WindSpeed10m != nil {
		cur.WindKmh = *raw.Current.WindSpeed10m
	}
	if raw.Current.IsDay != nil {
		cur.IsDay = *raw.Current.IsDay
	}
	cur.Summary = WMOCodeSummary(cur.Code)
	cur.Icon = IconKind(cur.Code)
	if cur.IsDay == 0 {
		// night: prefer moon-style icon for clear
		if cur.Code == 0 {
			cur.Icon = "night_clear"
		} else if cur.Icon == "clear" {
			cur.Icon = "night_clear"
		}
	}

	days := make([]Day, 0, len(raw.Daily.Time))
	for i := range raw.Daily.Time {
		if i >= len(raw.Daily.WeatherCode) || i >= len(raw.Daily.Temperature2mMax) || i >= len(raw.Daily.Temperature2mMin) {
			break
		}
		dateStr := raw.Daily.Time[i]
		t, err := time.ParseInLocation("2006-01-02", dateStr, locTZ)
		if err != nil {
			t = time.Now()
		}
		code := raw.Daily.WeatherCode[i]
		poP := 0
		if i < len(raw.Daily.PrecipitationProbMax) {
			poP = raw.Daily.PrecipitationProbMax[i]
		}
		days = append(days, Day{
			Date:    dateStr,
			Weekday: t.Format("Mon"),
			Code:    code,
			Summary: WMOCodeSummary(code),
			Icon:    IconKind(code),
			MaxC:    raw.Daily.Temperature2mMax[i],
			MinC:    raw.Daily.Temperature2mMin[i],
			PoP:     poP,
		})
	}

	out := &Snapshot{
		OK:         true,
		Location:   locName,
		Timezone:   tzName,
		FetchedAt:  time.Now().UTC().Format(time.RFC3339),
		Current:    &cur,
		Daily:      days,
		Attribution: "open-meteo.com",
	}
	return out, nil
}

func formatCoord(f float64) string {
	return strconv.FormatFloat(f, 'f', 4, 64)
}
