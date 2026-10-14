package web

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"botIAask/weather"
)

// weatherCacheTTL returns how long successful /api/weather JSON stays cached; driven by config (minutes, min 1).
func (s *Server) weatherCacheTTL() time.Duration {
	m := s.getConfig().Web.WeatherRefreshMinutes
	if m < 1 {
		m = 1
	}
	return time.Duration(m) * time.Minute
}

func (s *Server) clearWeatherCache() {
	s.weatherMu.Lock()
	s.weatherCache = nil
	s.weatherKey = ""
	s.weatherAt = time.Time{}
	s.weatherFail = nil
	s.weatherMu.Unlock()
}

// handleWeather returns JSON for the server-location weather panel (cached; Open-Meteo, no API key).
func (s *Server) handleWeather(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	loc := strings.TrimSpace(s.getConfig().Web.ServerLocation)
	if loc == "" {
		loc = "Barcelona, Spain"
	}
	writeJSON := func(b []byte) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(b)
	}

	// cached returns a fresh success payload, or the last failure while it is still cooling down.
	cached := func() ([]byte, bool) {
		s.weatherMu.Lock()
		defer s.weatherMu.Unlock()
		if len(s.weatherCache) > 0 && s.weatherKey == loc && time.Since(s.weatherAt) < s.weatherCacheTTL() {
			return append([]byte(nil), s.weatherCache...), true
		}
		if s.weatherFail != nil && time.Since(s.weatherFailAt) < weatherRetryAfter {
			return s.weatherFail, true
		}
		return nil, false
	}
	if b, ok := cached(); ok {
		writeJSON(b)
		return
	}

	// One upstream fetch at a time: concurrent misses (and a failing upstream) must not each
	// start their own 20s request from an unauthenticated endpoint.
	s.weatherFetchMu.Lock()
	defer s.weatherFetchMu.Unlock()
	if b, ok := cached(); ok {
		writeJSON(b)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	cl := s.weatherClient
	if cl == nil {
		cl = &http.Client{Timeout: 22 * time.Second}
	}
	snap, err := weather.FetchSnapshot(ctx, cl, loc)
	if err != nil {
		log.Printf("weather: %v", err)
		out, _ := json.Marshal(map[string]any{
			"ok":          false,
			"message":     "weather service temporarily unavailable",
			"attribution": "open-meteo.com",
		})
		s.weatherMu.Lock()
		s.weatherFail, s.weatherFailAt = out, time.Now()
		s.weatherMu.Unlock()
		writeJSON(out)
		return
	}
	out, err := json.Marshal(snap)
	if err != nil {
		http.Error(w, "encode error", http.StatusInternalServerError)
		return
	}
	s.weatherMu.Lock()
	if snap != nil && snap.OK {
		s.weatherCache = append([]byte(nil), out...)
		s.weatherKey = loc
		s.weatherAt = time.Now()
		s.weatherFail = nil
	} else {
		// A failed snapshot is only replayed for weatherRetryAfter, never cached like a success.
		s.weatherFail, s.weatherFailAt = out, time.Now()
	}
	s.weatherMu.Unlock()
	writeJSON(out)
}

// weatherRetryAfter is how long a failed Open-Meteo call is answered from memory before trying again.
const weatherRetryAfter = time.Minute
