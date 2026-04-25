package web

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"botIAask/progtodo"
)

// programmerTodoPublic is returned to unauthenticated and non-staff web clients (no staff-only field).
// IRC user suggestions (admin_only=0) and their review status are visible to everyone.
type programmerTodoPublic struct {
	ID           string    `json:"id"`
	Body         string    `json:"body"`
	CreatedAt    time.Time `json:"created_at"`
	AuthorNick   string    `json:"author_nick"`
	Importance   string    `json:"importance"`
	ReviewStatus string    `json:"review_status"`
	Disabled     bool      `json:"disabled"`
}

func progtodoEntriesToPublicView(items []progtodo.Entry) []programmerTodoPublic {
	out := make([]programmerTodoPublic, 0, len(items))
	for _, e := range items {
		if e.AdminOnly {
			continue
		}
		out = append(out, programmerTodoPublic{
			ID: e.ID, Body: e.Body, CreatedAt: e.CreatedAt, AuthorNick: e.AuthorNick,
			Importance: e.Importance, ReviewStatus: e.ReviewStatus, Disabled: e.Disabled,
		})
	}
	return out
}

func (s *Server) handleProgrammerTodos(w http.ResponseWriter, r *http.Request) {
	if s.progtodoDB == nil {
		http.Error(w, "Programmer TODO system not initialized", http.StatusServiceUnavailable)
		return
	}
	// Only privileged dashboard staff (same as /api/status staff_admin) see IRC staff-only + full moderation list.
	// Everyone else (including anonymous) sees user-submitted items (admin_only=0) to track public status.
	staff := s.staffAdminFromRequest(r)

	switch r.Method {
	case http.MethodGet:
		var list interface{}
		var err error
		if staff {
			list, err = s.progtodoDB.ListAll()
		} else {
			var pub []progtodo.Entry
			pub, err = s.progtodoDB.ListPublic()
			if err == nil {
				list = progtodoEntriesToPublicView(pub)
			}
		}
		if err != nil {
			log.Printf("programmer-todos list: %v", err)
			http.Error(w, "Failed to list", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"items": list, "staff": staff})

	case http.MethodPatch:
		if !s.staffAdminFromRequest(r) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		var body struct {
			ID           string `json:"id"`
			Importance   string `json:"importance"`
			ReviewStatus string `json:"review_status"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		if err := s.progtodoDB.UpdateStaff(body.ID, body.Importance, body.ReviewStatus); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"success": true})

	case http.MethodDelete:
		if !s.staffAdminFromRequest(r) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		if err := s.progtodoDB.DeleteByID(id); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"success": true})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}
