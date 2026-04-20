package web

import (
	"bufio"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"crypto/rand"
	"encoding/hex"

	"botIAask/bookmarks"
	"botIAask/config"
	"botIAask/irc"
	"botIAask/rss"
	"botIAask/stats"
	"botIAask/uploads"
)

//go:embed templates/*
var templatesFS embed.FS

// Server handles the web dashboard
type Server struct {
	cfg        *config.Config
	bot        *irc.Bot
	rssFetcher   *rss.Fetcher
	statsTracker *stats.Tracker
	bookmarksDB  *bookmarks.Database
	authDB       *AuthDatabase
	uploadsDB    *uploads.Database
	templates    *template.Template
}

// NewServer creates a new web server instance
func NewServer(cfg *config.Config, bot *irc.Bot, rssFetcher *rss.Fetcher, statsTracker *stats.Tracker, bookmarksDB *bookmarks.Database, uploadsDB *uploads.Database) *Server {
	tmpl, err := template.ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		log.Fatalf("Failed to parse templates: %v", err)
	}

	authDB, err := NewAuthDatabase("web_auth.db")
	if err != nil {
		log.Fatalf("Failed to initialize auth database: %v", err)
	}

	if err := authDB.CheckAndSeedInitialAdmin(cfg); err != nil {
		log.Printf("Warning: failed to seed initial admin: %v", err)
	}

	return &Server{
		cfg:          cfg,
		bot:          bot,
		rssFetcher:   rssFetcher,
		statsTracker: statsTracker,
		bookmarksDB:  bookmarksDB,
		authDB:       authDB,
		uploadsDB:    uploadsDB,
		templates:    tmpl,
	}
}

// Start starts the web server
func (s *Server) Start() error {
	mux := http.NewServeMux()

	// API endpoints
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/logs/stream", s.handleLogStream)
	mux.HandleFunc("/api/rss/toggle", s.handleRSSToggle)
	mux.HandleFunc("/api/stats/stream", s.handleStatsStream)
	mux.HandleFunc("/api/stats/toggle", s.handleStatsToggle)
	mux.HandleFunc("/api/stats/history", s.handleStatsHistory)
	mux.HandleFunc("/api/bookmarks", s.handleBookmarks)
	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/api/logout", s.handleLogout)
	mux.HandleFunc("/api/users", s.handleUsers)
	mux.HandleFunc("/api/users/password", s.handlePasswordUpdate)
	mux.HandleFunc("/api/pastes", s.handlePastesList)
	mux.HandleFunc("/api/pastes/delete", s.handlePasteDelete)
	mux.HandleFunc("/api/pastes/pending", s.handlePendingPastes)
	mux.HandleFunc("/api/pastes/approve", s.handlePasteApprove)
	mux.HandleFunc("/api/pastes/reject", s.handlePasteReject)

	// Upload/Paste routes
	mux.HandleFunc("/upload", s.handleUpload)
	mux.HandleFunc("/upload/cancel", s.handleUploadCancel)
	mux.HandleFunc("/p/", s.handlePasteView)

	// Static files (app.js)
	mux.HandleFunc("/static/", s.handleStatic)

	// Dashboard page
	mux.HandleFunc("/", s.handleDashboard)

	addr := fmt.Sprintf("%s:%d", s.cfg.Web.Host, s.cfg.Web.Port)
	log.Printf("Starting web dashboard on http://%s", addr)
	fmt.Printf("\n--------------------------------------------------\n")
	fmt.Printf("🚀 Web Dashboard: http://%s\n", addr)
	fmt.Printf("--------------------------------------------------\n\n")

	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	return server.ListenAndServe()
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	isAdmin, needsChange := s.checkAuth(r)
	status := map[string]interface{}{
		"uptime":      s.bot.GetUptime(),
		"connected":   true,
		"server":      s.cfg.IRC.Server,
		"nickname":    s.cfg.IRC.Nickname,
		"channels":    s.cfg.IRC.Channels,
		"ai_model":    s.cfg.AI.Model,
		"ai_status":   "Online",
		"ai_requests": s.bot.GetAIRequestCount(),
		"rss_enabled": s.rssFetcher.IsEnabled(),
		"stats_enabled": s.statsTracker.IsEnabled(),
		"start_time":  s.bot.GetStartTime().Format(time.RFC3339),
		"is_admin":    isAdmin,
		"needs_password_change": needsChange,
	}

	if isAdmin && s.statsTracker.IsEnabled() {
		nicks, chans := s.statsTracker.GetAdmins()
		status["admin_nicknames"] = nicks
		status["channel_admins"] = chans
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (s *Server) handleLogStream(w http.ResponseWriter, r *http.Request) {
	channel := r.URL.Query().Get("channel")
	if channel == "" {
		http.Error(w, "Channel is required", http.StatusBadRequest)
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Determine log file path for today
	// Replicate logger's safeChannel logic
	safeChannel := strings.ReplaceAll(channel, "/", "_")
	if len(safeChannel) > 0 && (safeChannel[0] == '#' || safeChannel[0] == '&') {
		safeChannel = safeChannel[1:]
	}
	
	today := time.Now().Format("2006-01-02")
	logFile := filepath.Join("logs", fmt.Sprintf("%s_%s.log", safeChannel, today))

	file, err := os.Open(logFile)
	if err != nil {
		fmt.Fprintf(w, "data: Error opening log file: %v\n\n", err)
		flusher.Flush()
		// We don't return here because the file might be created later
	}

	var reader *bufio.Reader
	if file != nil {
		reader = bufio.NewReader(file)
	}

	// Ticker for polling new data if file is at EOF
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	// Notify client that stream is open
	fmt.Fprintf(w, "data: [CONNECTED to %s]\n\n", channel)
	flusher.Flush()

	ctx := r.Context()

	for {
		select {
		case <-ctx.Done():
			if file != nil {
				file.Close()
			}
			return
		case <-ticker.C:
			// If file was not open or was closed, try to open/reopen it
			if file == nil {
				file, err = os.Open(logFile)
				if err == nil {
					reader = bufio.NewReader(file)
				} else {
					continue // Still no file
				}
			}

			// Read all available lines
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					if err == io.EOF {
						break // End of current data, wait for next tick
					}
					// Real error
					fmt.Fprintf(w, "data: [ERROR] %v\n\n", err)
					flusher.Flush()
					break
				}
				// Send line to client
				fmt.Fprintf(w, "data: %s", line) // line already has \n
				fmt.Fprint(w, "\n")              // data block ends with extra \n
				flusher.Flush()
			}
		}
	}
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/static/app.js" {
		data, err := templatesFS.ReadFile("templates/app.js")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/javascript")
		w.Write(data)
		return
	}
	http.NotFound(w, r)
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	err := s.templates.Execute(w, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleRSSToggle(w http.ResponseWriter, r *http.Request) {
	isAdmin, _ := s.checkAuth(r)
	if !isAdmin {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
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

func (s *Server) handleStatsToggle(w http.ResponseWriter, r *http.Request) {
	isAdmin, _ := s.checkAuth(r)
	if !isAdmin {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
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
	default:
		since = time.Now().Add(-1 * time.Hour)
	}

	history, err := s.statsTracker.GetHistory(since)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(history)
}

func (s *Server) handleStatsStream(w http.ResponseWriter, r *http.Request) {
	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Subscribe to stats updates
	statsChan := s.statsTracker.Subscribe()
	defer s.statsTracker.Unsubscribe(statsChan)

	// Send initial data if available (could fetch last entries from DB here)
	// For now, just wait for next tick

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case entry := <-statsChan:
			data, err := json.Marshal(entry)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", string(data))
			flusher.Flush()
		}
	}
}

func (s *Server) handleBookmarks(w http.ResponseWriter, r *http.Request) {
	if s.bookmarksDB == nil {
		http.Error(w, "Bookmarks database not initialized", http.StatusInternalServerError)
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

func (s *Server) checkAuth(r *http.Request) (bool, bool) {
	cookie, err := r.Cookie("admin_session")
	if err != nil {
		return false, false
	}
	_, needsChange, err := s.authDB.ValidateSession(cookie.Value)
	return err == nil, needsChange
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var creds struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&creds); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	userID, needsChange, err := s.authDB.Authenticate(creds.Username, creds.Password)
	if err != nil {
		http.Error(w, "Invalid credentials", http.StatusUnauthorized)
		return
	}

	token, err := s.authDB.CreateSession(userID)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "admin_session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(24 * time.Hour),
	})

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "needs_password_change": needsChange})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("admin_session")
	if err == nil {
		s.authDB.DeleteSession(cookie.Value)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "admin_session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	isAdmin, _ := s.checkAuth(r)
	if !isAdmin {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	switch r.Method {
	case http.MethodGet:
		users, err := s.authDB.GetUsers()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(users)

	case http.MethodPost:
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		if err := s.authDB.AddUser(req.Username, req.Password); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)

	case http.MethodPatch:
		var req struct {
			ID          string `json:"id"`
			NewPassword string `json:"new_password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		if err := s.authDB.UpdateUserPassword(req.ID, req.NewPassword); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)

	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "ID required", http.StatusBadRequest)
			return
		}
		if err := s.authDB.RemoveUser(id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)

	}
}

func (s *Server) handlePasswordUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	cookie, err := r.Cookie("admin_session")
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	userID, _, err := s.authDB.ValidateSession(cookie.Value)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		Password string `json:"password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	if err := s.authDB.UpdatePassword(userID, req.Password); err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "Token is required", http.StatusBadRequest)
		return
	}

	upload, err := s.uploadsDB.GetUploadByToken(token)
	if err != nil {
		http.Error(w, "Invalid or expired token", http.StatusNotFound)
		return
	}

	if upload.Status != "pending_form" {
		http.Error(w, "This token has already been used", http.StatusBadRequest)
		return
	}

	// Check 30 min expiration
	if time.Since(upload.CreatedAt) > 30*time.Minute {
		http.Error(w, "This token has expired", http.StatusBadRequest)
		return
	}

	if r.Method == http.MethodGet {
		s.templates.ExecuteTemplate(w, "upload.html", map[string]interface{}{
			"Upload":  upload,
			"BaseURL": s.cfg.Web.BaseURL,
		})
		return
	}

	if r.Method == http.MethodPost {
		title := r.FormValue("title")
		desc := r.FormValue("description")
		content := r.FormValue("content")
		expiresStr := r.FormValue("expires")

		expiresDays := 7
		fmt.Sscanf(expiresStr, "%d", &expiresDays)

		// Create ticket ID
		ticketID := generateHex(4)

		err := s.uploadsDB.SubmitUpload(token, ticketID, title, desc, content, expiresDays)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Notify IRC channel and admins
		s.bot.SendMessage(upload.Channel, fmt.Sprintf("\x0307[UPLOAD]\x03 New ticket pending approval: %s (by %s)", ticketID, upload.Username))
		s.bot.NotifyAdmins(fmt.Sprintf("\x0307[TICKET]\x03 New pending approval: %s. Use !ticket approve %s to publish.", ticketID, ticketID))

		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, "<html><body style='font-family:sans-serif;background:#0f172a;color:white;display:flex;flex-direction:column;align-items:center;justify-content:center;height:100vh;'>")
		fmt.Fprintf(w, "<h1 style='color:#38bdf8;'>Successfully submitted</h1>")
		fmt.Fprintf(w, "<p>Ticket ID: <strong>%s</strong></p>", ticketID)
		fmt.Fprintf(w, "<p>Please wait for admin approval in IRC.</p>")
		fmt.Fprintf(w, "</body></html>")
	}
}

func (s *Server) handleUploadCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := r.FormValue("token")
	username, channel, err := s.uploadsDB.CancelUploadByToken(token)
	if err != nil {
		http.Error(w, "Error cancelling", http.StatusInternalServerError)
		return
	}
	s.bot.SendMessage(channel, fmt.Sprintf("\x0304[CANCEL]\x03 User %s cancelled their upload session.", username))
	
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, "<html><body style='font-family:sans-serif;background:#0f172a;color:white;display:flex;flex-direction:column;align-items:center;justify-content:center;height:100vh;'>")
	fmt.Fprintf(w, "<h1 style='color:#f87171;'>Session Cancelled</h1>")
	fmt.Fprintf(w, "<p>The bot has been notified.</p>")
	fmt.Fprintf(w, "</body></html>")
}

func (s *Server) handlePasteView(w http.ResponseWriter, r *http.Request) {
	ticketID := strings.TrimPrefix(r.URL.Path, "/p/")
	if ticketID == "" {
		http.NotFound(w, r)
		return
	}

	upload, err := s.uploadsDB.GetUploadByTicketID(ticketID)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	if upload.Status != "approved" {
		http.Error(w, "This paste is pending approval or was cancelled", http.StatusForbidden)
		return
	}

	// Check expiration if days > 0
	if upload.ExpiresInDays > 0 {
		if upload.ApprovedAt.Valid && time.Since(upload.ApprovedAt.Time) > time.Duration(upload.ExpiresInDays)*24*time.Hour {
			http.Error(w, "This paste has expired", http.StatusGone)
			return
		}
	}

	content, err := os.ReadFile(upload.ContentPath)
	if err != nil {
		http.Error(w, "Error reading content", http.StatusInternalServerError)
		return
	}

	data := struct {
		Upload  *uploads.Upload
		Content string
	}{
		Upload:  upload,
		Content: string(content),
	}

	s.templates.ExecuteTemplate(w, "paste.html", data)
}

func (s *Server) handlePastesList(w http.ResponseWriter, r *http.Request) {
	if s.uploadsDB == nil {
		http.Error(w, "Uploads database not initialized", http.StatusInternalServerError)
		return
	}

	pageStr := r.URL.Query().Get("page")
	page := 1
	if p, err := fmt.Sscanf(pageStr, "%d", &page); err == nil && p > 0 {
		if page < 1 {
			page = 1
		}
	}

	limit := 10
	offset := (page - 1) * limit

	items, total, err := s.uploadsDB.GetApprovedPastes(limit, offset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	totalPages := (total + limit - 1) / limit

	response := map[string]interface{}{
		"pastes":      items,
		"page":        page,
		"total_pages": totalPages,
		"total_count": total,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func (s *Server) handlePasteDelete(w http.ResponseWriter, r *http.Request) {
	isAdmin, _ := s.checkAuth(r)
	if !isAdmin {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ticketID := r.URL.Query().Get("ticketID")
	if ticketID == "" {
		http.Error(w, "ticketID required", http.StatusBadRequest)
		return
	}

	if err := s.uploadsDB.DeletePaste(ticketID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handlePendingPastes(w http.ResponseWriter, r *http.Request) {
	isAdmin, _ := s.checkAuth(r)
	if !isAdmin {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if s.uploadsDB == nil {
		http.Error(w, "Uploads database not initialized", http.StatusInternalServerError)
		return
	}

	items, err := s.uploadsDB.GetPendingTickets()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

func (s *Server) handlePasteApprove(w http.ResponseWriter, r *http.Request) {
	isAdmin, _ := s.checkAuth(r)
	if !isAdmin {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ticketID := r.URL.Query().Get("ticketID")
	if ticketID == "" {
		http.Error(w, "ticketID required", http.StatusBadRequest)
		return
	}

	if err := s.uploadsDB.ApproveTicket(ticketID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Notify IRC as well
	upload, err := s.uploadsDB.GetUploadByTicketID(ticketID)
	if err == nil {
		s.bot.SendMessage(upload.Channel, fmt.Sprintf("\x0303[APPROVED]\x03 Ticket %s has been approved and published: %s/p/%s", ticketID, s.cfg.Web.BaseURL, ticketID))
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handlePasteReject(w http.ResponseWriter, r *http.Request) {
	isAdmin, _ := s.checkAuth(r)
	if !isAdmin {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ticketID := r.URL.Query().Get("ticketID")
	if ticketID == "" {
		http.Error(w, "ticketID required", http.StatusBadRequest)
		return
	}

	if err := s.uploadsDB.CancelTicket(ticketID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Notify IRC
	upload, err := s.uploadsDB.GetUploadByTicketID(ticketID)
	if err == nil {
		s.bot.SendMessage(upload.Channel, fmt.Sprintf("\x0304[REJECTED]\x03 Ticket %s was rejected by an administrator.", ticketID))
	}

	w.WriteHeader(http.StatusOK)
}

func generateHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
