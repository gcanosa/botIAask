package web

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"botIAask/bookmarks"
	"botIAask/config"
	"botIAask/crypto"
	"botIAask/irc"
	"botIAask/logger"
	"botIAask/meta"
	"botIAask/rss"
	"botIAask/stats"
	"botIAask/uploads"
)

//go:embed templates/*
var templatesFS embed.FS

// Server handles the web dashboard
type Server struct {
	cfgMu        sync.RWMutex
	cfg          *config.Config
	bot          *irc.Bot
	rssFetcher   *rss.Fetcher
	statsTracker *stats.Tracker
	bookmarksDB  *bookmarks.Database
	authDB       *AuthDatabase
	uploadsDB    *uploads.Database
	cryptoDB     *crypto.Database
	templates    *template.Template
	forexCache   map[string]float64
	forexUpdate  time.Time

	cryptoChartCache map[string]cryptoChartCacheEntry
	cryptoChartMu    sync.Mutex

	forexChartCache map[string]cryptoChartCacheEntry
	forexChartMu    sync.Mutex

	marketChartRawMu    sync.Mutex
	marketChartRawCache map[string]marketChartRawCacheEntry

	rehashFn func() error
}

type marketChartRawCacheEntry struct {
	at  time.Time
	pts [][2]float64
}

func marketChartRawKey(geckoID, days string) string {
	return geckoID + ":" + days
}

type cryptoChartCacheEntry struct {
	at   time.Time
	body []byte
}

func (s *Server) getConfig() *config.Config {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg
}

// SetConfig replaces the in-memory config after a live reload (shared pointer with IRC/RSS).
func (s *Server) SetConfig(cfg *config.Config) {
	s.cfgMu.Lock()
	s.cfg = cfg
	s.cfgMu.Unlock()
}

// NewServer creates a new web server instance
func NewServer(cfg *config.Config, bot *irc.Bot, rssFetcher *rss.Fetcher, statsTracker *stats.Tracker, bookmarksDB *bookmarks.Database, uploadsDB *uploads.Database, cryptoDB *crypto.Database, rehashFn func() error) *Server {
	tmpl, err := template.ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		log.Fatalf("Failed to parse templates: %v", err)
	}

	authDB, err := NewAuthDatabase("data/web_auth.db")
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
		cryptoDB:     cryptoDB,
		templates:    tmpl,
		rehashFn:     rehashFn,
	}
}

// Start starts the web server
func (s *Server) Start() error {
	mux := http.NewServeMux()

	// API endpoints
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/logs/catalog", s.handleLogCatalog)
	mux.HandleFunc("/api/logs/history", s.handleLogHistory)
	mux.HandleFunc("/api/logs/stream", s.handleLogStream)
	mux.HandleFunc("/api/rss/toggle", s.handleRSSToggle)
	mux.HandleFunc("/api/rss/news", s.handleRSSNews)
	mux.HandleFunc("/api/rss/fetch", s.handleRSSFetchNow)
	mux.HandleFunc("/api/rss/settings", s.handleRSSSettings)
	mux.HandleFunc("/api/rehash", s.handleRehash)
	mux.HandleFunc("/api/irc/channels/reveal", s.handleIRCChannelReveal)
	mux.HandleFunc("/api/irc/channels/announce", s.handleIRCChannelAnnounce)
	mux.HandleFunc("/api/irc/channels/autojoin", s.handleIRCChannelAutojoin)
	mux.HandleFunc("/api/irc/channels/session", s.handleIRCChannelSession)
	mux.HandleFunc("/api/irc/channels", s.handleIRCChannels)
	mux.HandleFunc("/api/config/irc-admins", s.handleConfigIRCAdmins)
	mux.HandleFunc("/api/stats/stream", s.handleStatsStream)
	mux.HandleFunc("/api/stats/toggle", s.handleStatsToggle)
	mux.HandleFunc("/api/stats/history", s.handleStatsHistory)
	mux.HandleFunc("/api/bookmarks", s.handleBookmarks)
	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/api/logout", s.handleLogout)
	mux.HandleFunc("/api/me/ui-theme", s.handleUITheme)
	mux.HandleFunc("/api/users", s.handleUsers)
	mux.HandleFunc("/api/users/password", s.handlePasswordUpdate)
	mux.HandleFunc("/api/pastes", s.handlePastesList)
	mux.HandleFunc("/api/pastes/delete", s.handlePasteDelete)
	mux.HandleFunc("/api/pastes/pending", s.handlePendingPastes)
	mux.HandleFunc("/api/pastes/approve", s.handlePasteApprove)
	mux.HandleFunc("/api/pastes/reject", s.handlePasteReject)
	mux.HandleFunc("/api/uploads/files", s.handleUploadsFilesList)
	mux.HandleFunc("/api/uploads/files/pending", s.handleUploadsFilesPending)
	mux.HandleFunc("/api/uploads/files/compress", s.handleUploadFileCompress)
	mux.HandleFunc("/api/uploads/detail", s.handleUploadDetail)
	mux.HandleFunc("/api/uploads/public", s.handleUploadsPublic)
	mux.HandleFunc("/api/uploads/settings", s.handleUploadSettings)
	mux.HandleFunc("/api/finance", s.handleFinance)
	mux.HandleFunc("/api/finance/crypto-chart", s.handleCryptoChart)
	mux.HandleFunc("/api/finance/forex-chart", s.handleForexChart)

	// Upload/Paste routes
	mux.HandleFunc("/upload", s.handleUpload)
	mux.HandleFunc("/upload/cancel", s.handleUploadCancel)
	mux.HandleFunc("/f/", s.handleFileDownload)
	mux.HandleFunc("/p/", s.handlePasteView)

	// Static files (app.js)
	mux.HandleFunc("/static/", s.handleStatic)

	// Dashboard page
	mux.HandleFunc("/", s.handleDashboard)

	cfg := s.getConfig()
	addr := fmt.Sprintf("%s:%d", cfg.Web.Host, cfg.Web.Port)
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
	sessionOK, staffAdmin, needsChange := s.sessionStaffInfo(r)
	pendingPastes, pendingUploads := 0, 0
	if staffAdmin && s.uploadsDB != nil {
		var err error
		pendingPastes, err = s.uploadsDB.CountPendingPastes()
		if err != nil {
			log.Printf("pending pastes count: %v", err)
			pendingPastes = 0
		}
		pendingUploads, err = s.uploadsDB.CountPendingFiles()
		if err != nil {
			log.Printf("pending uploads count: %v", err)
			pendingUploads = 0
		}
	}

	status := map[string]interface{}{
		"version":               meta.Version,
		"uptime":                s.bot.GetUptime(),
		"connected":             s.bot.IsConnected(),
		"server":                s.getConfig().IRC.Server,
		"nickname":              s.getConfig().IRC.Nickname,
		"channels":              s.getConfig().IRC.Channels,
		"ai_model":              s.getConfig().AI.Model,
		"ai_status":             "Online",
		"ai_requests":           s.bot.GetAIRequestCount(),
		"rss_enabled":           s.rssFetcher.IsEnabled(),
		"stats_enabled":         s.statsTracker.IsEnabled(),
		"start_time":            s.bot.GetStartTime().Format(time.RFC3339),
		"is_admin":              sessionOK,
		"staff_admin":           staffAdmin,
		"needs_password_change": needsChange,
		"irc_authenticated":     s.bot.IsAuthenticated(),
		"pending_pastes":        pendingPastes,
		"pending_uploads":       pendingUploads,
	}

	if staffAdmin && s.statsTracker.IsEnabled() {
		ircNicks, chans := s.statsTracker.GetAdmins()
		webNames, err := s.authDB.ActiveSessionUsernames()
		if err != nil {
			log.Printf("active web admin sessions: %v", err)
		}
		status["irc_admin_nicknames"] = ircNicks
		status["web_admin_usernames"] = webNames
		status["channel_admins"] = chans
	}

	if sessionOK {
		uiTheme := "dark"
		if cookie, err := r.Cookie("admin_session"); err == nil {
			if uid, _, err := s.authDB.ValidateSession(cookie.Value); err == nil {
				if t, err := s.authDB.GetUITheme(uid); err == nil && t != "" {
					uiTheme = t
				} else if err != nil {
					log.Printf("ui_theme: %v", err)
				}
			}
		}
		status["ui_theme"] = uiTheme
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (s *Server) handleUITheme(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	isAdmin, _ := s.checkAuth(r)
	if !isAdmin {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
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

	var body struct {
		Theme string `json:"theme"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	switch body.Theme {
	case "dark", "light", "mono":
	default:
		http.Error(w, "Invalid theme", http.StatusBadRequest)
		return
	}
	if err := s.authDB.SetUITheme(userID, body.Theme); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleLogStream(w http.ResponseWriter, r *http.Request) {
	channel := strings.TrimSpace(r.URL.Query().Get("channel"))
	if channel == "" {
		http.Error(w, "Channel is required", http.StatusBadRequest)
		return
	}

	date := strings.TrimSpace(r.URL.Query().Get("date"))
	localToday := time.Now().Format("2006-01-02")
	if date == "" {
		date = localToday
	}
	if _, err := time.ParseInLocation("2006-01-02", date, time.Local); err != nil {
		http.Error(w, "invalid date", http.StatusBadRequest)
		return
	}
	if date != localToday {
		http.Error(w, "live stream is only available for server local today; use /api/logs/history for past dates", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	key := logger.ChannelFileKey(channel, s.getConfig().IRC.Server)
	logFile := filepath.Join("logs", fmt.Sprintf("%s_%s.log", key, date))

	var file *os.File
	var reader *bufio.Reader
	var err error
	file, err = os.Open(logFile)
	if err != nil {
		fmt.Fprintf(w, "data: Error opening log file: %v\n\n", err)
		flusher.Flush()
	} else {
		reader = bufio.NewReader(file)
	}

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	fmt.Fprintf(w, "data: [CONNECTED %s %s]\n\n", channel, date)
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
			newToday := time.Now().Format("2006-01-02")
			if newToday != date {
				date = newToday
				logFile = filepath.Join("logs", fmt.Sprintf("%s_%s.log", key, date))
				if file != nil {
					file.Close()
					file = nil
					reader = nil
				}
				fmt.Fprintf(w, "data: [ROLLOVER %s]\n\n", date)
				flusher.Flush()
			}

			if file == nil {
				file, err = os.Open(logFile)
				if err == nil {
					reader = bufio.NewReader(file)
				} else {
					continue
				}
			}

			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					if err == io.EOF {
						break
					}
					fmt.Fprintf(w, "data: [ERROR] %v\n\n", err)
					flusher.Flush()
					break
				}
				fmt.Fprintf(w, "data: %s", line)
				fmt.Fprint(w, "\n")
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
	if path == "/static/style.css" {
		data, err := templatesFS.ReadFile("templates/style.css")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/css")
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

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	err := s.templates.ExecuteTemplate(w, "index.html", nil)
	if err != nil {
		log.Printf("ExecuteTemplate index.html error: %v", err)
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
		isAdmin, _ := s.checkAuth(r)
		if !isAdmin {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
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
	isAdmin, _ := s.checkAuth(r)
	if !isAdmin {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	go s.rssFetcher.Fetch()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "Fetching started"})
}

func (s *Server) handleRehash(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	isAdmin, _ := s.checkAuth(r)
	if !isAdmin {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if s.rehashFn == nil {
		http.Error(w, "Rehash unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := s.rehashFn(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// applyConfigFromFile reloads config from disk and applies it to IRC, RSS, and stats (no AI update, no IRC admin NOTICE). Mirrors main rehash except notifications.
func (s *Server) applyConfigFromFile(path string) error {
	newCfg, err := config.LoadConfig(path)
	if err != nil {
		return err
	}
	s.SetConfig(newCfg)
	if s.bot != nil {
		s.bot.ApplyLiveConfig(newCfg)
	}
	if s.rssFetcher != nil {
		s.rssFetcher.ApplyConfig(newCfg)
	}
	if s.statsTracker != nil {
		s.statsTracker.ApplyConfig(newCfg)
	}
	if s.rssFetcher != nil {
		if db := s.rssFetcher.GetDB(); db != nil {
			if err := db.RepairEmptySourceHackerNewsWhenSingleHNFeed(newCfg.RSS.FeedURLs); err != nil {
				log.Printf("RSS: repair source column (web): %v", err)
			}
		}
	}
	return nil
}

func ircWebChannelNameOK(s string) bool {
	s = strings.TrimSpace(s)
	return len(s) > 0 && (s[0] == '#' || s[0] == '&')
}

type ircChannelRow struct {
	Name         string `json:"name"`
	HasPassword  bool   `json:"has_password"`
	AnnounceRSS  bool   `json:"announce_rss"`
	AutoJoin     bool   `json:"auto_join"`
}

type ircSessionRow struct {
	Name        string `json:"name"`
	HasPassword bool   `json:"has_password"`
}

func (s *Server) handleConfigIRCAdmins(w http.ResponseWriter, r *http.Request) {
	isAdmin, _ := s.checkAuth(r)
	if !isAdmin {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodGet:
		cfg := s.getConfig()
		list := append([]string(nil), cfg.Admin.Admins...)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string][]string{"hostmasks": list})

	case http.MethodPost:
		var req struct {
			Hostmask string `json:"hostmask"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		h := strings.TrimSpace(req.Hostmask)
		if h == "" {
			http.Error(w, "hostmask required", http.StatusBadRequest)
			return
		}
		s.cfgMu.Lock()
		for _, ex := range s.cfg.Admin.Admins {
			if ex == h {
				s.cfgMu.Unlock()
				http.Error(w, "already in list", http.StatusConflict)
				return
			}
		}
		s.cfg.Admin.Admins = append(s.cfg.Admin.Admins, h)
		if err := config.SaveConfig(config.DefaultConfigPath, s.cfg); err != nil {
			s.cfg.Admin.Admins = s.cfg.Admin.Admins[:len(s.cfg.Admin.Admins)-1]
			s.cfgMu.Unlock()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.cfgMu.Unlock()
		if err := s.applyConfigFromFile(config.DefaultConfigPath); err != nil {
			http.Error(w, "Saved but failed to apply: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})

	case http.MethodDelete:
		raw := strings.TrimSpace(r.URL.Query().Get("hostmask"))
		if raw == "" {
			http.Error(w, "hostmask required", http.StatusBadRequest)
			return
		}
		if dec, err := url.QueryUnescape(raw); err == nil && dec != "" {
			raw = strings.TrimSpace(dec)
		}
		s.cfgMu.Lock()
		found := -1
		for i, ex := range s.cfg.Admin.Admins {
			if ex == raw {
				found = i
				break
			}
		}
		if found < 0 {
			s.cfgMu.Unlock()
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		s.cfg.Admin.Admins = append(s.cfg.Admin.Admins[:found], s.cfg.Admin.Admins[found+1:]...)
		if err := config.SaveConfig(config.DefaultConfigPath, s.cfg); err != nil {
			s.cfgMu.Unlock()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.cfgMu.Unlock()
		if err := s.applyConfigFromFile(config.DefaultConfigPath); err != nil {
			http.Error(w, "Saved but failed to apply: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleIRCChannels(w http.ResponseWriter, r *http.Request) {
	isAdmin, _ := s.checkAuth(r)
	if !isAdmin {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	cfg := s.getConfig()
	switch r.Method {
	case http.MethodGet:
		rows := make([]ircChannelRow, 0, len(cfg.IRC.Channels))
		for _, ch := range cfg.IRC.Channels {
			rows = append(rows, ircChannelRow{
				Name:         ch.Name,
				HasPassword:  ch.Password != "",
				AnnounceRSS:  config.RSSChannelContainsFold(cfg.RSS.Channels, ch.Name),
				AutoJoin:     ch.AutoJoinEnabled(),
			})
		}
		sessRows := make([]ircSessionRow, 0)
		if s.bot != nil {
			sess := s.bot.ListSessionChannels()
			for _, ch := range sess {
				sessRows = append(sessRows, ircSessionRow{Name: ch.Name, HasPassword: ch.Password != ""})
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"channels": rows, "session_channels": sessRows})
		return

	case http.MethodPost:
		var req struct {
			Name         string `json:"name"`
			Password     string `json:"password"`
			AutoJoin     *bool  `json:"auto_join"`
			SessionOnly  bool   `json:"session_only"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(req.Name)
		if !ircWebChannelNameOK(name) {
			http.Error(w, "Invalid channel name (use #chan or &chan)", http.StatusBadRequest)
			return
		}
		if req.SessionOnly {
			if s.bot == nil {
				http.Error(w, "Bot unavailable", http.StatusServiceUnavailable)
				return
			}
			if err := s.bot.JoinChannelSession(config.IRChannel{Name: name, Password: req.Password}); err != nil {
				if strings.Contains(err.Error(), "already") {
					http.Error(w, err.Error(), http.StatusConflict)
					return
				}
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true, "session": true})
			return
		}
		s.cfgMu.Lock()
		for _, ex := range s.cfg.IRC.Channels {
			if strings.EqualFold(ex.Name, name) {
				s.cfgMu.Unlock()
				http.Error(w, "Channel already in autoinjoin list", http.StatusConflict)
				return
			}
		}
		entry := config.IRChannel{Name: name, Password: req.Password, AutoJoin: req.AutoJoin}
		s.cfg.IRC.Channels = append(s.cfg.IRC.Channels, entry)
		if err := config.SaveConfig(config.DefaultConfigPath, s.cfg); err != nil {
			s.cfgMu.Unlock()
			http.Error(w, "Failed to save config: "+err.Error(), http.StatusInternalServerError)
			return
		}
		s.cfgMu.Unlock()
		if err := s.applyConfigFromFile(config.DefaultConfigPath); err != nil {
			http.Error(w, "Saved but failed to apply: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		return

	case http.MethodDelete:
		name := strings.TrimSpace(r.URL.Query().Get("name"))
		if d, err := url.QueryUnescape(name); err == nil && d != "" {
			name = strings.TrimSpace(d)
		}
		if !ircWebChannelNameOK(name) {
			http.Error(w, "Invalid or missing name query", http.StatusBadRequest)
			return
		}
		s.cfgMu.Lock()
		canon, ok := config.FindIRChannelByName(s.cfg.IRC.Channels, name)
		if !ok {
			s.cfgMu.Unlock()
			http.Error(w, "Channel not found", http.StatusNotFound)
			return
		}
		var out []config.IRChannel
		for _, ex := range s.cfg.IRC.Channels {
			if strings.EqualFold(ex.Name, name) {
				continue
			}
			out = append(out, ex)
		}
		s.cfg.IRC.Channels = out
		s.cfg.RSS.Channels = config.SetRSSChannelAnnounce(s.cfg.RSS.Channels, canon.Name, false, "")
		if err := config.SaveConfig(config.DefaultConfigPath, s.cfg); err != nil {
			s.cfgMu.Unlock()
			http.Error(w, "Failed to save config: "+err.Error(), http.StatusInternalServerError)
			return
		}
		s.cfgMu.Unlock()
		if err := s.applyConfigFromFile(config.DefaultConfigPath); err != nil {
			http.Error(w, "Saved but failed to apply: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		return

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleIRCChannelReveal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	isAdmin, _ := s.checkAuth(r)
	if !isAdmin {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	if !ircWebChannelNameOK(name) {
		http.Error(w, "Invalid channel name", http.StatusBadRequest)
		return
	}
	if ch, ok := config.FindIRChannelByName(s.getConfig().IRC.Channels, name); ok {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"password": ch.Password})
		return
	}
	if s.bot != nil {
		for _, ch := range s.bot.ListSessionChannels() {
			if strings.EqualFold(ch.Name, name) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]string{"password": ch.Password})
				return
			}
		}
	}
	http.Error(w, "Channel not found", http.StatusNotFound)
}

func (s *Server) handleIRCChannelAnnounce(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut && r.Method != http.MethodPatch {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	isAdmin, _ := s.checkAuth(r)
	if !isAdmin {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		Name     string `json:"name"`
		Announce *bool  `json:"announce"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Announce == nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	if !ircWebChannelNameOK(name) {
		http.Error(w, "Invalid channel name", http.StatusBadRequest)
		return
	}
	s.cfgMu.Lock()
	entry, ok := config.FindIRChannelByName(s.cfg.IRC.Channels, name)
	if !ok {
		s.cfgMu.Unlock()
		http.Error(w, "Channel not in autoinjoin list", http.StatusNotFound)
		return
	}
	canon := entry.Name
	s.cfg.RSS.Channels = config.SetRSSChannelAnnounce(s.cfg.RSS.Channels, name, *req.Announce, canon)
	if err := config.SaveConfig(config.DefaultConfigPath, s.cfg); err != nil {
		s.cfgMu.Unlock()
		http.Error(w, "Failed to save config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.cfgMu.Unlock()
	if s.rssFetcher != nil {
		s.rssFetcher.ApplyConfig(s.getConfig())
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (s *Server) handleIRCChannelAutojoin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut && r.Method != http.MethodPatch {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	isAdmin, _ := s.checkAuth(r)
	if !isAdmin {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		Name     string `json:"name"`
		AutoJoin *bool  `json:"auto_join"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.AutoJoin == nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	if !ircWebChannelNameOK(name) {
		http.Error(w, "Invalid channel name", http.StatusBadRequest)
		return
	}
	s.cfgMu.Lock()
	updated := false
	for i, ex := range s.cfg.IRC.Channels {
		if strings.EqualFold(ex.Name, name) {
			s.cfg.IRC.Channels[i].AutoJoin = req.AutoJoin
			updated = true
			break
		}
	}
	if !updated {
		s.cfgMu.Unlock()
		http.Error(w, "Channel not found", http.StatusNotFound)
		return
	}
	if err := config.SaveConfig(config.DefaultConfigPath, s.cfg); err != nil {
		s.cfgMu.Unlock()
		http.Error(w, "Failed to save config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.cfgMu.Unlock()
	if err := s.applyConfigFromFile(config.DefaultConfigPath); err != nil {
		http.Error(w, "Saved but failed to apply: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (s *Server) handleIRCChannelSession(w http.ResponseWriter, r *http.Request) {
	isAdmin, _ := s.checkAuth(r)
	if !isAdmin {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if s.bot == nil {
		http.Error(w, "Bot unavailable", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodPost:
		var req struct {
			Name     string `json:"name"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(req.Name)
		if !ircWebChannelNameOK(name) {
			http.Error(w, "Invalid channel name (use #chan or &chan)", http.StatusBadRequest)
			return
		}
		if err := s.bot.JoinChannelSession(config.IRChannel{Name: name, Password: req.Password}); err != nil {
			if strings.Contains(err.Error(), "already") {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		return
	case http.MethodDelete:
		name := strings.TrimSpace(r.URL.Query().Get("name"))
		if d, err := url.QueryUnescape(name); err == nil && d != "" {
			name = strings.TrimSpace(d)
		}
		if !ircWebChannelNameOK(name) {
			http.Error(w, "Invalid or missing name query", http.StatusBadRequest)
			return
		}
		if err := s.bot.PartChannelSession(name); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		return
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleRSSSettings(w http.ResponseWriter, r *http.Request) {
	isAdmin, _ := s.checkAuth(r)
	if !isAdmin {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if r.Method == http.MethodGet {
		response := map[string]interface{}{
			"interval_minutes": s.getConfig().RSS.IntervalMinutes,
			"retention_count":  s.getConfig().RSS.RetentionCount,
			"feed_urls":        s.getConfig().RSS.FeedURLs,
			"announce_to_irc":  s.getConfig().RSS.AnnounceToIRCEnabled(),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
		return
	}

	if r.Method == http.MethodPost {
		var req struct {
			IntervalMinutes int      `json:"interval_minutes"`
			RetentionCount  int      `json:"retention_count"`
			FeedURLs        []string `json:"feed_urls"`
			AnnounceToIRC   *bool    `json:"announce_to_irc,omitempty"`
		}

		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}

		s.cfgMu.Lock()
		oldInterval := s.cfg.RSS.IntervalMinutes
		s.cfg.RSS.IntervalMinutes = req.IntervalMinutes
		s.cfg.RSS.RetentionCount = req.RetentionCount
		s.cfg.RSS.FeedURLs = req.FeedURLs
		if req.AnnounceToIRC != nil {
			v := *req.AnnounceToIRC
			s.cfg.RSS.AnnounceToIRC = &v
		}
		if err := config.SaveConfig(config.DefaultConfigPath, s.cfg); err != nil {
			s.cfgMu.Unlock()
			http.Error(w, "Failed to save config: "+err.Error(), http.StatusInternalServerError)
			return
		}
		s.cfgMu.Unlock()

		// Restart fetcher if enabled and interval changed
		if s.rssFetcher.IsEnabled() && oldInterval != req.IntervalMinutes {
			s.rssFetcher.SetEnabled(false)
			s.rssFetcher.SetEnabled(true)
		}

		// Always trigger cleanup if retention changed
		if req.RetentionCount > 0 {
			s.rssFetcher.GetDB().Cleanup(req.RetentionCount)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]bool{"success": true})
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
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
	case "6m":
		since = time.Now().AddDate(0, -6, 0)
	case "1y":
		since = time.Now().AddDate(-1, 0, 0)
	default:
		since = time.Now().Add(-1 * time.Hour)
	}

	history, err := s.statsTracker.GetHistory(since)
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

	// Replay recent DB rows so the client does not wait for the next snapshot tick
	since := time.Now().Add(-2 * time.Hour)
	bootstrap, err := s.statsTracker.GetHistory(since)
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

	if r.Method == http.MethodDelete {
		isAdmin, _ := s.checkAuth(r)
		if !isAdmin {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
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

func (s *Server) checkAuth(r *http.Request) (bool, bool) {
	cookie, err := r.Cookie("admin_session")
	if err != nil {
		return false, false
	}
	_, needsChange, err := s.authDB.ValidateSession(cookie.Value)
	return err == nil, needsChange
}

func isPrivilegedAdminRole(role string) bool {
	r := strings.ToLower(strings.TrimSpace(role))
	return r == "" || r == "admin"
}

// sessionStaffInfo returns dashboard session validity, whether the user has staff (moderation) privileges, and password-change flag.
func (s *Server) sessionStaffInfo(r *http.Request) (sessionOK, staffAdmin bool, needsPasswordChange bool) {
	cookie, err := r.Cookie("admin_session")
	if err != nil {
		return false, false, false
	}
	uid, needs, err := s.authDB.ValidateSession(cookie.Value)
	if err != nil {
		return false, false, false
	}
	role, err := s.authDB.GetUserRole(uid)
	if err != nil {
		return true, false, needs
	}
	return true, isPrivilegedAdminRole(role), needs
}

func (s *Server) staffAdminFromRequest(r *http.Request) bool {
	_, staff, _ := s.sessionStaffInfo(r)
	return staff
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
	if err := s.authDB.TouchLastLogin(userID); err != nil {
		log.Printf("last_login: %v", err)
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
	if !s.staffAdminFromRequest(r) {
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

func (s *Server) effectiveMaxFileMB() int {
	mb := s.getConfig().Uploads.MaxFileMB
	if mb <= 0 {
		return 200
	}
	return mb
}

func (s *Server) maxUploadBytes() int64 {
	return int64(s.effectiveMaxFileMB()) * 1024 * 1024
}

// multipartSlack adds headroom for multipart boundaries and small form fields.
const multipartSlack int64 = 1 << 20

func (s *Server) clientHostFromRequest(r *http.Request) string {
	if s.getConfig().Web.TrustForwardedFor {
		xff := r.Header.Get("X-Forwarded-For")
		if xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[0])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) pathWithinDir(path, dir string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absDir, absPath)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func parseUploadToken(r *http.Request) string {
	raw := strings.TrimSpace(r.URL.Query().Get("token"))
	if raw == "" {
		return ""
	}
	if dec, err := url.QueryUnescape(raw); err == nil {
		raw = strings.TrimSpace(dec)
	}
	return raw
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if s.uploadsDB == nil {
		http.Error(w, "Uploads are not configured on this server", http.StatusServiceUnavailable)
		return
	}
	token := parseUploadToken(r)
	if token == "" {
		http.Error(w, "Token is required", http.StatusBadRequest)
		return
	}

	upload, err := s.uploadsDB.GetUploadByToken(token)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			log.Printf("upload: no session for token (len=%d); ensure web and IRC use the same uploads DB (see uploads.db_path in config)", len(token))
		} else {
			log.Printf("upload: token lookup failed: %v", err)
		}
		http.Error(w, "Invalid or expired token", http.StatusNotFound)
		return
	}

	if upload.Status != "pending_form" {
		http.Error(w, "This token has already been used", http.StatusBadRequest)
		return
	}

	if time.Since(upload.CreatedAt) > 30*time.Minute {
		http.Error(w, "This token has expired", http.StatusBadRequest)
		return
	}

	if upload.IsFile() {
		s.handleUploadFile(w, r, token, upload)
		return
	}

	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		err := s.templates.ExecuteTemplate(w, "upload.html", map[string]interface{}{
			"Upload":  upload,
			"BaseURL": s.getConfig().Web.BaseURL,
		})
		if err != nil {
			log.Printf("ExecuteTemplate upload.html error: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	if r.Method == http.MethodPost {
		title := r.FormValue("title")
		desc := r.FormValue("description")
		content := r.FormValue("content")
		expiresStr := r.FormValue("expires")

		expiresDays := 7
		fmt.Sscanf(expiresStr, "%d", &expiresDays)

		ticketID := generateHex(4)

		err := s.uploadsDB.SubmitUpload(token, ticketID, title, desc, content, expiresDays, s.clientHostFromRequest(r))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

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

func (s *Server) handleUploadFile(w http.ResponseWriter, r *http.Request, token string, sess *uploads.Upload) {
	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		err := s.templates.ExecuteTemplate(w, "file_upload.html", map[string]interface{}{
			"Upload":    sess,
			"BaseURL":   s.getConfig().Web.BaseURL,
			"MaxFileMB": s.effectiveMaxFileMB(),
		})
		if err != nil {
			log.Printf("ExecuteTemplate file_upload.html error: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	limit := s.maxUploadBytes() + multipartSlack
	r.Body = http.MaxBytesReader(w, r.Body, limit)

	mem := int64(32 << 20)
	if mem > limit {
		mem = limit
	}
	if err := r.ParseMultipartForm(mem); err != nil {
		var mbErr *http.MaxBytesError
		if errors.As(err, &mbErr) || errors.Is(err, io.ErrUnexpectedEOF) {
			http.Error(w, "Upload too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "Bad multipart form", http.StatusBadRequest)
		return
	}

	file, hdr, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "File is required", http.StatusBadRequest)
		return
	}
	defer file.Close()

	if hdr.Size > s.maxUploadBytes() {
		http.Error(w, "File too large", http.StatusRequestEntityTooLarge)
		return
	}

	title := r.FormValue("title")
	if strings.TrimSpace(title) == "" {
		http.Error(w, "Title is required", http.StatusBadRequest)
		return
	}
	desc := r.FormValue("description")
	expiresStr := r.FormValue("expires")
	expiresDays := 7
	fmt.Sscanf(expiresStr, "%d", &expiresDays)

	ticketID := generateHex(4)
	ext := uploads.SafeFileExt(hdr.Filename)
	diskName := ticketID + ext
	diskPath := filepath.Join(s.uploadsDB.FilesDiskDir(), diskName)

	out, err := os.Create(diskPath)
	if err != nil {
		http.Error(w, "Could not store file", http.StatusInternalServerError)
		return
	}
	n, err := io.Copy(out, io.LimitReader(file, s.maxUploadBytes()+1))
	out.Close()
	if err != nil {
		os.Remove(diskPath)
		http.Error(w, "Error saving file", http.StatusInternalServerError)
		return
	}
	if n > s.maxUploadBytes() {
		os.Remove(diskPath)
		http.Error(w, "File too large", http.StatusRequestEntityTooLarge)
		return
	}
	if n == 0 {
		os.Remove(diskPath)
		http.Error(w, "Empty file", http.StatusBadRequest)
		return
	}

	ctype := hdr.Header.Get("Content-Type")
	if ctype == "" || ctype == "application/octet-stream" {
		ctype = ""
	}

	mdH, shH, err := uploads.HexMD5SHA256FromFile(diskPath)
	if err != nil {
		os.Remove(diskPath)
		http.Error(w, "Error hashing file", http.StatusInternalServerError)
		return
	}

	err = s.uploadsDB.SubmitFileUpload(token, ticketID, title, desc, expiresDays, diskPath, hdr.Filename, ctype, n, s.clientHostFromRequest(r), mdH, shH)
	if err != nil {
		os.Remove(diskPath)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.bot.SendMessage(sess.Channel, fmt.Sprintf("\x0307[UPLOAD]\x03 New file pending approval: %s (by %s)", ticketID, sess.Username))
	s.bot.NotifyAdmins(fmt.Sprintf("\x0307[TICKET]\x03 New file pending approval: %s. Use !ticket approve %s to publish.", ticketID, ticketID))

	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, "<html><body style='font-family:sans-serif;background:#0f172a;color:white;display:flex;flex-direction:column;align-items:center;justify-content:center;height:100vh;'>")
	fmt.Fprintf(w, "<h1 style='color:#38bdf8;'>Successfully submitted</h1>")
	fmt.Fprintf(w, "<p>Ticket ID: <strong>%s</strong></p>", ticketID)
	fmt.Fprintf(w, "<p>Please wait for admin approval.</p>")
	fmt.Fprintf(w, "</body></html>")
}

func (s *Server) handleUploadCancel(w http.ResponseWriter, r *http.Request) {
	if s.uploadsDB == nil {
		http.Error(w, "Uploads are not configured on this server", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := strings.TrimSpace(r.FormValue("token"))
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

	if upload.IsFile() {
		http.NotFound(w, r)
		return
	}

	if upload.Status != "approved" {
		http.Error(w, "This paste is pending approval or was cancelled", http.StatusForbidden)
		return
	}

	if upload.IsAccessExpired(time.Now()) && !s.staffAdminFromRequest(r) {
		http.Error(w, "This paste has expired", http.StatusGone)
		return
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

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	err = s.templates.ExecuteTemplate(w, "paste.html", data)
	if err != nil {
		log.Printf("ExecuteTemplate paste.html error: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// approvedUploadListJSON adds public-expiry fields for dashboard paste/file tables.
type approvedUploadListJSON struct {
	*uploads.Upload
	ExpiresAt *string `json:"expires_at,omitempty"`
	IsExpired bool    `json:"is_expired"`
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

	staff := s.staffAdminFromRequest(r)
	items, total, err := s.uploadsDB.GetApprovedPastes(limit, offset, time.Now(), !staff)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	sessionOK, _ := s.checkAuth(r)
	if !sessionOK {
		redacted := make([]*uploads.Upload, len(items))
		for i, u := range items {
			c := *u
			c.ClientHost = ""
			redacted[i] = &c
		}
		items = redacted
	}

	now := time.Now()
	out := make([]approvedUploadListJSON, len(items))
	for i, u := range items {
		row := approvedUploadListJSON{Upload: u, IsExpired: u.IsAccessExpired(now)}
		if exp, ok := u.AccessExpiresAt(); ok {
			expStr := exp.UTC().Format(time.RFC3339)
			row.ExpiresAt = &expStr
		}
		out[i] = row
	}

	totalPages := (total + limit - 1) / limit

	response := map[string]interface{}{
		"pastes":      out,
		"page":        page,
		"total_pages": totalPages,
		"total_count": total,
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	json.NewEncoder(w).Encode(response)
}

func (s *Server) handlePasteDelete(w http.ResponseWriter, r *http.Request) {
	if !s.staffAdminFromRequest(r) {
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
	if !s.staffAdminFromRequest(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if s.uploadsDB == nil {
		http.Error(w, "Uploads database not initialized", http.StatusInternalServerError)
		return
	}

	items, err := s.uploadsDB.GetPendingPastes()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	json.NewEncoder(w).Encode(items)
}

func (s *Server) handlePasteApprove(w http.ResponseWriter, r *http.Request) {
	if !s.staffAdminFromRequest(r) {
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

	upload, err := s.uploadsDB.GetUploadByTicketID(ticketID)
	if err == nil {
		pubURL := fmt.Sprintf("%s/p/%s", s.getConfig().Web.BaseURL, ticketID)
		if upload.IsFile() {
			pubURL = fmt.Sprintf("%s/f/%s", s.getConfig().Web.BaseURL, ticketID)
		}
		s.bot.SendMessage(upload.Channel, fmt.Sprintf("\x0303[APPROVED]\x03 Ticket %s has been approved and published: %s", ticketID, pubURL))
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handlePasteReject(w http.ResponseWriter, r *http.Request) {
	if !s.staffAdminFromRequest(r) {
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

func (s *Server) handleFinance(w http.ResponseWriter, r *http.Request) {
	data := map[string]interface{}{}

	// Get Crypto Prices
	data["crypto"] = []crypto.PriceEntry{}
	var cryptoLastUpdate time.Time
	if s.cryptoDB != nil {
		prices, err := s.cryptoDB.GetLatestPrices()
		if err == nil && len(prices) > 0 {
			data["crypto"] = prices
			cryptoLastUpdate = prices[0].FetchedAt
		}
	}
	data["crypto_last_update"] = cryptoLastUpdate.Format(time.RFC3339)

	// Get Forex Rates with simple server-side caching (1 hour).
	// If cache is empty, retry on every request until at least one rate is fetched.
	if s.forexCache == nil || len(s.forexCache) == 0 || time.Since(s.forexUpdate) > 1*time.Hour {
		forex := map[string]float64{}

		// EUR to USD
		if eurRates, err := irc.FetchRates("EUR"); err == nil {
			if rate, ok := eurRates.Rates["USD"]; ok {
				forex["eur_usd"] = rate
			}
		}

		// USD to ARS (Official)
		if usdRates, err := irc.FetchRates("USD"); err == nil {
			if rate, ok := usdRates.Rates["ARS"]; ok {
				forex["usd_ars"] = rate
			}
			if eurRate, ok := usdRates.Rates["EUR"]; ok && eurRate != 0 {
				forex["eur_ars"] = usdRates.Rates["ARS"] / eurRate
			}
		}

		// ARS Blue (Parallel) - Using Bluelytics API
		client := &http.Client{Timeout: 5 * time.Second}
		if resp, err := client.Get("https://api.bluelytics.com.ar/v2/latest"); err == nil {
			defer resp.Body.Close()
			var blueData struct {
				Blue struct {
					ValueAvg float64 `json:"value_avg"`
				} `json:"blue"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&blueData); err == nil {
				forex["usd_ars_blue"] = blueData.Blue.ValueAvg
			}
		}

		if len(forex) > 0 {
			s.forexCache = forex
			s.forexUpdate = time.Now()
			if s.cryptoDB != nil {
				if err := s.cryptoDB.SaveForexSnapshot(forex, s.forexUpdate); err != nil {
					log.Printf("forex snapshot save: %v", err)
				}
			}
		}
	}

	forexOut := map[string]float64{}
	if s.forexCache != nil {
		for k, v := range s.forexCache {
			forexOut[k] = v
		}
	}
	// Fill gaps (e.g. official USD/ARS) from DB when the live API omits a key but history exists.
	if s.cryptoDB != nil {
		if dbFX, err := s.cryptoDB.GetLatestForexPerKey(); err == nil {
			for k, v := range dbFX {
				if _, ok := forexOut[k]; !ok {
					forexOut[k] = v
				}
			}
		}
	}
	data["forex"] = forexOut
	data["forex_last_update"] = s.forexUpdate.Format(time.RFC3339)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func (s *Server) handleCryptoChart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.cryptoDB == nil {
		http.Error(w, "crypto unavailable", http.StatusServiceUnavailable)
		return
	}

	rangeKey := crypto.NormalizeRangeKey(r.URL.Query().Get("range"))
	if rangeKey == "" {
		rangeKey = "1w"
	}
	if _, err := crypto.RangeToWindow(rangeKey); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	const chartTTL = 10 * time.Minute
	s.cryptoChartMu.Lock()
	if s.cryptoChartCache != nil {
		if e, ok := s.cryptoChartCache[rangeKey]; ok && time.Since(e.at) < chartTTL {
			s.cryptoChartMu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.Write(e.body)
			return
		}
	}
	s.cryptoChartMu.Unlock()

	prices, err := s.cryptoDB.GetLatestPrices()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	days, err := crypto.RangeToCoinGeckoDays(rangeKey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	client := &http.Client{Timeout: 20 * time.Second}
	raw := make([]crypto.MarketRawSeries, 0, len(prices))

	// CoinGecko free tier rate-limits hard on burst parallel calls. We cache raw
	// market_chart by (coin, days) so 6h/1d and 3d/1w reuse the same upstream data,
	// and we fetch sequentially with a short pause between network calls.
	for _, p := range prices {
		if p.GeckoID == "" {
			continue
		}
		key := marketChartRawKey(p.GeckoID, days)
		var pts [][2]float64
		cacheRead := time.Now()
		s.marketChartRawMu.Lock()
		if s.marketChartRawCache != nil {
			if e, ok := s.marketChartRawCache[key]; ok && cacheRead.Sub(e.at) < chartTTL {
				pts = e.pts
			}
		}
		s.marketChartRawMu.Unlock()

		if pts == nil {
			fetched, ferr := crypto.FetchMarketChartWithRetry(client, p.GeckoID, days)
			if ferr != nil || len(fetched) < 2 {
				time.Sleep(150 * time.Millisecond)
				continue
			}
			pts = fetched
			clipAt := time.Now()
			s.marketChartRawMu.Lock()
			if s.marketChartRawCache == nil {
				s.marketChartRawCache = make(map[string]marketChartRawCacheEntry)
			}
			s.marketChartRawCache[key] = marketChartRawCacheEntry{at: clipAt, pts: pts}
			s.marketChartRawMu.Unlock()
			time.Sleep(150 * time.Millisecond)
		}

		raw = append(raw, crypto.MarketRawSeries{
			Symbol:  p.Symbol,
			GeckoID: p.GeckoID,
			Points:  pts,
		})
	}

	resp, err := crypto.BuildChartResponse(rangeKey, raw, time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	body, err := json.Marshal(resp)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.cryptoChartMu.Lock()
	if s.cryptoChartCache == nil {
		s.cryptoChartCache = make(map[string]cryptoChartCacheEntry)
	}
	s.cryptoChartCache[rangeKey] = cryptoChartCacheEntry{at: time.Now(), body: body}
	s.cryptoChartMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}

func (s *Server) handleForexChart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.cryptoDB == nil {
		http.Error(w, "crypto unavailable", http.StatusServiceUnavailable)
		return
	}

	rangeKey := crypto.NormalizeRangeKey(r.URL.Query().Get("range"))
	if rangeKey == "" {
		rangeKey = "1w"
	}
	win, err := crypto.RangeToWindow(rangeKey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	const chartTTL = 5 * time.Minute
	s.forexChartMu.Lock()
	if s.forexChartCache != nil {
		if e, ok := s.forexChartCache[rangeKey]; ok && time.Since(e.at) < chartTTL {
			s.forexChartMu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.Write(e.body)
			return
		}
	}
	s.forexChartMu.Unlock()

	since := time.Now().Add(-win)
	rows, err := s.cryptoDB.GetForexHistorySince(since)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp, err := crypto.BuildForexChartResponse(rangeKey, rows, time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	body, err := json.Marshal(resp)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.forexChartMu.Lock()
	if s.forexChartCache == nil {
		s.forexChartCache = make(map[string]cryptoChartCacheEntry)
	}
	s.forexChartCache[rangeKey] = cryptoChartCacheEntry{at: time.Now(), body: body}
	s.forexChartMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}

func contentDispositionAttachment(orig, ticketID, pathExt string) string {
	name := filepath.Base(orig)
	if name == "" || name == "." || name == "/" {
		if pathExt != "" {
			name = ticketID + pathExt
		} else {
			name = ticketID
		}
	}
	name = strings.ReplaceAll(name, `"`, `'`)
	name = strings.ReplaceAll(name, "\r", "")
	name = strings.ReplaceAll(name, "\n", "")
	if len(name) > 180 {
		name = name[:180]
	}
	return fmt.Sprintf(`attachment; filename="%s"`, name)
}

func (s *Server) handleFileDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ticketID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/f/"), "/")
	if ticketID == "" {
		http.NotFound(w, r)
		return
	}
	upload, err := s.uploadsDB.GetUploadByTicketID(ticketID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !upload.IsFile() {
		http.NotFound(w, r)
		return
	}
	if upload.Status != "approved" {
		http.Error(w, "This file is pending approval or was cancelled", http.StatusForbidden)
		return
	}
	if !upload.IsPublic && !s.staffAdminFromRequest(r) {
		http.Error(w, "This file is not publicly accessible", http.StatusForbidden)
		return
	}
	if upload.IsAccessExpired(time.Now()) && !s.staffAdminFromRequest(r) {
		http.Error(w, "This file has expired", http.StatusGone)
		return
	}
	f, err := os.Open(upload.ContentPath)
	if err != nil {
		http.Error(w, "Error reading file", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	ctype := upload.ContentType
	if ctype == "" {
		buf := make([]byte, 512)
		n, _ := f.Read(buf)
		ctype = http.DetectContentType(buf[:n])
		_, _ = f.Seek(0, 0)
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", contentDispositionAttachment(upload.OriginalFilename, ticketID, filepath.Ext(upload.ContentPath)))
	_, copyErr := io.Copy(w, f)
	if copyErr != nil {
		log.Printf("file download %s: %v", ticketID, copyErr)
		return
	}
	if err := s.uploadsDB.IncrementFileDownloadCount(ticketID); err != nil {
		log.Printf("increment download count %s: %v", ticketID, err)
	}
}

func (s *Server) handleUploadsFilesList(w http.ResponseWriter, r *http.Request) {
	if s.uploadsDB == nil {
		http.Error(w, "Uploads database not initialized", http.StatusInternalServerError)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
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
	publicOnly := !s.staffAdminFromRequest(r)
	items, total, err := s.uploadsDB.GetApprovedFiles(limit, offset, publicOnly)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if publicOnly {
		for _, u := range items {
			u.ClientHost = ""
		}
	}
	now := time.Now()
	fileRows := make([]approvedUploadListJSON, len(items))
	for i, u := range items {
		row := approvedUploadListJSON{Upload: u, IsExpired: u.IsAccessExpired(now)}
		if exp, ok := u.AccessExpiresAt(); ok {
			expStr := exp.UTC().Format(time.RFC3339)
			row.ExpiresAt = &expStr
		}
		fileRows[i] = row
	}
	totalPages := (total + limit - 1) / limit
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"files":       fileRows,
		"page":        page,
		"total_pages": totalPages,
		"total_count": total,
	})
}

func (s *Server) handleUploadsFilesPending(w http.ResponseWriter, r *http.Request) {
	if !s.staffAdminFromRequest(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if s.uploadsDB == nil {
		http.Error(w, "Uploads database not initialized", http.StatusInternalServerError)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	items, err := s.uploadsDB.GetPendingFiles()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	json.NewEncoder(w).Encode(items)
}

func uploadAlreadyCompressed(u *uploads.Upload) bool {
	lower := strings.ToLower(u.OriginalFilename)
	if strings.HasSuffix(lower, ".tgz") || strings.HasSuffix(lower, ".tar.gz") {
		return true
	}
	ct := strings.ToLower(u.ContentType)
	return strings.Contains(ct, "gzip") || strings.Contains(ct, "x-gzip")
}

func uploadDetailCanCompress(u *uploads.Upload) bool {
	return u.IsFile() && !uploadAlreadyCompressed(u)
}

func writeSingleFileTgz(dstPath, srcPath, memberName string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()
	st, err := src.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("source is not a regular file")
	}
	dst, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		_ = dst.Close()
		if cleanup {
			_ = os.Remove(dstPath)
		}
	}()
	gw := gzip.NewWriter(dst)
	tw := tar.NewWriter(gw)
	hdr := &tar.Header{
		Name:    memberName,
		Mode:    0644,
		Size:    st.Size(),
		ModTime: st.ModTime(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		_ = tw.Close()
		_ = gw.Close()
		return err
	}
	if _, err := io.Copy(tw, src); err != nil {
		_ = tw.Close()
		_ = gw.Close()
		return err
	}
	if err := tw.Close(); err != nil {
		_ = gw.Close()
		return err
	}
	if err := gw.Close(); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func (s *Server) handleUploadDetail(w http.ResponseWriter, r *http.Request) {
	if s.uploadsDB == nil {
		http.Error(w, "Uploads database not initialized", http.StatusInternalServerError)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ticketID := strings.TrimSpace(r.URL.Query().Get("ticketID"))
	if ticketID == "" {
		http.Error(w, "ticketID required", http.StatusBadRequest)
		return
	}
	u, err := s.uploadsDB.GetUploadByTicketID(ticketID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if u.Status != "approved" && u.Status != "pending_approval" {
		http.NotFound(w, r)
		return
	}
	staff := s.staffAdminFromRequest(r)
	if u.Status == "pending_approval" && !staff {
		http.NotFound(w, r)
		return
	}
	if u.Status == "approved" && u.IsFile() && !u.IsPublic && !staff {
		http.NotFound(w, r)
		return
	}
	displayName := u.OriginalFilename
	if displayName == "" {
		displayName = filepath.Base(u.ContentPath)
	}
	now := time.Now()
	expired := u.IsAccessExpired(now)
	var viewPath, downloadPath *string
	if u.Status == "approved" {
		if u.IsFile() {
			p := "/f/" + u.TicketID
			if staff || !expired {
				downloadPath = &p
			}
		} else {
			p := "/p/" + u.TicketID
			if staff || !expired {
				viewPath = &p
			}
		}
	}
	var approvedAt *string
	if u.ApprovedAt.Valid {
		s := u.ApprovedAt.Time.UTC().Format(time.RFC3339)
		approvedAt = &s
	}
	var expiresAt *string
	if exp, ok := u.AccessExpiresAt(); ok {
		s := exp.UTC().Format(time.RFC3339)
		expiresAt = &s
	}
	clientHost := u.ClientHost
	if !staff {
		clientHost = ""
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ticket_id":         u.TicketID,
		"public_ref":        u.PublicRef,
		"paste_kind":        u.PasteKind,
		"upload_type":       u.UploadType,
		"status":            u.Status,
		"display_filename":  displayName,
		"title":             u.Title,
		"username":          u.Username,
		"client_host":       clientHost,
		"approved_at":       approvedAt,
		"expires_in_days":   u.ExpiresInDays,
		"expires_at":        expiresAt,
		"is_expired":        expired,
		"size_bytes":        u.SizeBytes,
		"md5_hex":           u.MD5Hex,
		"sha256_hex":        u.SHA256Hex,
		"is_file":           u.IsFile(),
		"is_public":         u.IsPublic,
		"can_compress":      staff && u.Status == "approved" && uploadDetailCanCompress(u),
		"download_path":     downloadPath,
		"view_path":         viewPath,
		"download_count":    u.DownloadCount,
		"original_filename": u.OriginalFilename,
	})
}

func (s *Server) handleUploadsPublic(w http.ResponseWriter, r *http.Request) {
	if !s.staffAdminFromRequest(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if s.uploadsDB == nil {
		http.Error(w, "Uploads database not initialized", http.StatusInternalServerError)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		TicketID string `json:"ticket_id"`
		Public   bool   `json:"is_public"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	ticketID := strings.TrimSpace(req.TicketID)
	if ticketID == "" {
		http.Error(w, "ticket_id required", http.StatusBadRequest)
		return
	}
	if err := s.uploadsDB.SetFileIsPublic(ticketID, req.Public); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (s *Server) handleUploadFileCompress(w http.ResponseWriter, r *http.Request) {
	if !s.staffAdminFromRequest(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if s.uploadsDB == nil {
		http.Error(w, "Uploads database not initialized", http.StatusInternalServerError)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ticketID := strings.TrimSpace(r.URL.Query().Get("ticketID"))
	if ticketID == "" {
		http.Error(w, "ticketID required", http.StatusBadRequest)
		return
	}
	u, err := s.uploadsDB.GetUploadByTicketID(ticketID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !u.IsFile() || u.Status != "approved" {
		http.Error(w, "Not an approved file", http.StatusBadRequest)
		return
	}
	if !uploadDetailCanCompress(u) {
		http.Error(w, "File is already compressed", http.StatusBadRequest)
		return
	}
	oldPath := u.ContentPath
	filesDir := s.uploadsDB.FilesDiskDir()
	if !s.pathWithinDir(oldPath, filesDir) {
		http.Error(w, "Invalid file path", http.StatusInternalServerError)
		return
	}
	if _, err := os.Stat(oldPath); err != nil {
		http.Error(w, "File missing on disk", http.StatusNotFound)
		return
	}
	baseName := strings.TrimSpace(u.OriginalFilename)
	if baseName == "" {
		baseName = filepath.Base(oldPath)
	}
	memberName := filepath.Base(baseName)
	if memberName == "" || memberName == "." {
		memberName = ticketID + filepath.Ext(oldPath)
	}
	newName := baseName + ".tgz"
	newPath := filepath.Join(filesDir, ticketID+".tgz")
	if err := writeSingleFileTgz(newPath, oldPath, memberName); err != nil {
		log.Printf("compress %s: %v", ticketID, err)
		http.Error(w, "Compress failed", http.StatusInternalServerError)
		return
	}
	if err := os.Remove(oldPath); err != nil {
		log.Printf("compress remove old %s: %v", ticketID, err)
		_ = os.Remove(newPath)
		http.Error(w, "Could not replace original file", http.StatusInternalServerError)
		return
	}
	mdH, shH, err := uploads.HexMD5SHA256FromFile(newPath)
	if err != nil {
		http.Error(w, "Hash error", http.StatusInternalServerError)
		return
	}
	st, err := os.Stat(newPath)
	if err != nil {
		http.Error(w, "Stat error", http.StatusInternalServerError)
		return
	}
	if err := s.uploadsDB.ReplaceApprovedFileContent(ticketID, newPath, newName, "application/gzip", st.Size(), mdH, shH); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	u2, err := s.uploadsDB.GetUploadByTicketID(ticketID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(u2)
}

func (s *Server) handleUploadSettings(w http.ResponseWriter, r *http.Request) {
	if !s.staffAdminFromRequest(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]int{"max_file_mb": s.effectiveMaxFileMB()})
	case http.MethodPost:
		var req struct {
			MaxFileMB int `json:"max_file_mb"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		if req.MaxFileMB < 1 || req.MaxFileMB > 2048 {
			http.Error(w, "max_file_mb must be between 1 and 2048", http.StatusBadRequest)
			return
		}
		s.cfgMu.Lock()
		s.cfg.Uploads.MaxFileMB = req.MaxFileMB
		if err := config.SaveConfig(config.DefaultConfigPath, s.cfg); err != nil {
			s.cfgMu.Unlock()
			http.Error(w, "Failed to save config: "+err.Error(), http.StatusInternalServerError)
			return
		}
		s.cfgMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"success": true})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func generateHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
