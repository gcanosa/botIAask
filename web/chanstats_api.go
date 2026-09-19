package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// handleChanStats serves the Channel Stats page: GET /api/chanstats?days=30&network=&channel=
// Admin session required (it exposes nick-level activity).
func (s *Server) handleChanStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if ok, _ := s.checkAuth(r); !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days < 1 {
		days = 30
	}
	if days > 366 {
		days = 366
	}
	rep, err := s.statsTracker.ChanReport(days, r.URL.Query().Get("network"), strings.ToLower(r.URL.Query().Get("channel")))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rep)
}
