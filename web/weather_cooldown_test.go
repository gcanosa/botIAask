package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"botIAask/config"
)

type failingTransport struct{ calls atomic.Int32 }

func (f *failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	f.calls.Add(1)
	return nil, errors.New("upstream down")
}

// While Open-Meteo is down, concurrent anonymous requests share one upstream attempt and the
// failure is replayed for a minute instead of triggering a fresh 20s call every time.
func TestWeatherFailureIsCoolingDown(t *testing.T) {
	ft := &failingTransport{}
	s := &Server{cfg: &config.Config{}, weatherClient: &http.Client{Transport: ft}}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			s.handleWeather(rec, httptest.NewRequest(http.MethodGet, "/api/weather", nil))
			if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
				t.Errorf("status %d body %q", rec.Code, rec.Body.String())
			}
		}()
	}
	wg.Wait()
	first := ft.calls.Load()
	if first == 0 || first > 2 { // geocode (+ at most one retry inside FetchSnapshot)
		t.Fatalf("expected a single upstream attempt, got %d calls", first)
	}
	rec := httptest.NewRecorder()
	s.handleWeather(rec, httptest.NewRequest(http.MethodGet, "/api/weather", nil))
	if ft.calls.Load() != first {
		t.Fatal("request inside the cool-down hit the upstream again")
	}
}
