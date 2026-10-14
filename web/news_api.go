package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"botIAask/internal/guard"
)

func (s *Server) handleRSSToggle(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	enabled := !s.rssFetcher.IsEnabled()
	s.rssFetcher.SetEnabled(enabled)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"rss_enabled": enabled})
}

func (s *Server) handleGitHubToggle(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	enabled := !s.githubFetcher.IsEnabled()
	s.githubFetcher.SetEnabled(enabled)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"github_enabled": enabled})
}

func (s *Server) handleRSSNews(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		pageStr := r.URL.Query().Get("page")
		query := r.URL.Query().Get("q")
		page := 1
		fmt.Sscanf(pageStr, "%d", &page)
		if page < 1 {
			page = 1
		}

		limit := 15
		offset := (page - 1) * limit

		items, total, err := s.rssFetcher.GetDB().GetNews(limit, offset, query)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		lastFetch := s.rssFetcher.GetLastFetchTime()

		response := map[string]interface{}{
			"news":        items,
			"page":        page,
			"total_pages": (total + limit - 1) / limit,
			"total_count": total,
			"last_fetch":  lastFetch.Format(time.RFC3339),
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
		return
	}

	if r.Method == http.MethodDelete {
		if !s.requireAdmin(w, r) {
			return
		}

		guid := r.URL.Query().Get("guid")
		if guid == "" {
			http.Error(w, "GUID required", http.StatusBadRequest)
			return
		}

		if err := s.rssFetcher.GetDB().DeleteEntry(guid); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

func (s *Server) handleRSSFetchNow(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	guard.Go("rss fetch now", s.rssFetcher.Fetch)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "Fetching started"})
}

func (s *Server) handleBookmarks(w http.ResponseWriter, r *http.Request) {
	if s.bookmarksDB == nil {
		http.Error(w, "Bookmarks database not initialized", http.StatusInternalServerError)
		return
	}

	if r.Method == http.MethodDelete {
		if !s.requireAdmin(w, r) {
			return
		}
		idStr := r.URL.Query().Get("id")
		id, err := strconv.Atoi(idStr)
		if idStr == "" || err != nil || id < 1 {
			http.Error(w, "Invalid ID", http.StatusBadRequest)
			return
		}
		if err := s.bookmarksDB.DeleteBookmark(id); err != nil {
			if strings.Contains(err.Error(), "not found") {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	pageStr := r.URL.Query().Get("page")
	query := r.URL.Query().Get("q")
	page := 1
	if p, err := fmt.Sscanf(pageStr, "%d", &page); err == nil && p > 0 {
		if page < 1 {
			page = 1
		}
	}

	limit := 10
	offset := (page - 1) * limit

	total, err := s.bookmarksDB.GetBookmarksCount(query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	items, err := s.bookmarksDB.GetBookmarks(limit, offset, query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	totalPages := (total + limit - 1) / limit

	response := map[string]interface{}{
		"bookmarks":   items,
		"page":        page,
		"total_pages": totalPages,
		"total_count": total,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}
