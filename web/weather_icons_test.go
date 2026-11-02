package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Every icon name app.js can ask for must be embedded and served as SVG.
func TestWeatherIconsServed(t *testing.T) {
	s := &Server{}
	for _, n := range []string{"clear-day", "clear-night", "partly-cloudy-day", "partly-cloudy-night", "cloudy", "fog",
		"rain", "snow", "partly-cloudy-day-rain", "partly-cloudy-night-rain", "thunderstorms-rain"} {
		rec := httptest.NewRecorder()
		s.handleStatic(rec, httptest.NewRequest(http.MethodGet, "/static/weather/"+n+".svg", nil))
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/svg+xml" || !strings.Contains(rec.Body.String(), "<svg") {
			t.Errorf("%s: code %d type %q", n, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
}
