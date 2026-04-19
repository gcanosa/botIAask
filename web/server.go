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

	"botIAask/bookmarks"
	"botIAask/config"
	"botIAask/irc"
	"botIAask/rss"
	"botIAask/stats"
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
	templates    *template.Template
}

// NewServer creates a new web server instance
func NewServer(cfg *config.Config, bot *irc.Bot, rssFetcher *rss.Fetcher, statsTracker *stats.Tracker, bookmarksDB *bookmarks.Database) *Server {
	tmpl, err := template.ParseFS(templatesFS, "templates/index.html")
	if err != nil {
		log.Fatalf("Failed to parse templates: %v", err)
	}

	return &Server{
		cfg:          cfg,
		bot:          bot,
		rssFetcher:   rssFetcher,
		statsTracker: statsTracker,
		bookmarksDB:  bookmarksDB,
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
