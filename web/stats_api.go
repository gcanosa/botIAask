package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"botIAask/stats"
)

func (s *Server) handleStatsToggle(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	enabled := !s.statsTracker.IsEnabled()
	s.statsTracker.SetEnabled(enabled)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"stats_enabled": enabled})
}

func (s *Server) handleStatsHistory(w http.ResponseWriter, r *http.Request) {
	timeframe := r.URL.Query().Get("timeframe")
	network := r.URL.Query().Get("network") // "" = aggregate across all networks (default)
	var since time.Time

	switch timeframe {
	case "1h":
		since = time.Now().Add(-1 * time.Hour)
	case "6h":
		since = time.Now().Add(-6 * time.Hour)
	case "1d":
		since = time.Now().AddDate(0, 0, -1)
	case "5d":
		since = time.Now().AddDate(0, 0, -5)
	case "1m":
		since = time.Now().AddDate(0, -1, 0)
	case "6m":
		since = time.Now().AddDate(0, -6, 0)
	case "1y":
		since = time.Now().AddDate(-1, 0, 0)
	default:
		since = time.Now().Add(-1 * time.Hour)
	}

	history, err := s.statsTracker.GetHistory(since, network)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if history == nil {
		history = []stats.StatEntry{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(history)
}

func (s *Server) handleStatsStream(w http.ResponseWriter, r *http.Request) {
	network := r.URL.Query().Get("network") // "" = aggregate across all networks (default)

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Subscribe to stats updates. Every tick, the tracker broadcasts one merged
	// (Network == "") entry plus one per configured network; keep only the series this
	// client asked for so a client unaware of the network dimension (network == "") keeps
	// seeing exactly the single aggregate stream it always has.
	statsChan := s.statsTracker.Subscribe()
	defer s.statsTracker.Unsubscribe(statsChan)

	// Replay recent DB rows so the client does not wait for the next snapshot tick
	since := time.Now().Add(-2 * time.Hour)
	bootstrap, err := s.statsTracker.GetHistory(since, network)
	if err == nil {
		for _, entry := range bootstrap {
			data, err := json.Marshal(entry)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", string(data))
			flusher.Flush()
		}
	}

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case entry := <-statsChan:
			if entry.Network != network {
				continue
			}
			data, err := json.Marshal(entry)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", string(data))
			flusher.Flush()
		}
	}
}
