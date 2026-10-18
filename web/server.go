package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"botIAask/ai"
	"botIAask/bookmarks"
	"botIAask/config"
	"botIAask/crypto"
	"botIAask/github"
	"botIAask/internal/guard"
	"botIAask/irc"
	"botIAask/meta"
	"botIAask/progtodo"
	"botIAask/rss"
	"botIAask/shortlinks"
	"botIAask/stats"
	"botIAask/uploads"
)

//go:embed templates/*
var templatesFS embed.FS

// Server handles the web dashboard
type Server struct {
	compressing      sync.Map // ticketID -> struct{}: in-flight compress jobs
	cfgMu            sync.RWMutex
	cfg              *config.Config
	bot              *irc.Bot
	rssFetcher       *rss.Fetcher
	statsTracker     *stats.Tracker
	bookmarksDB      *bookmarks.Database
	authDB           *AuthDatabase
	uploadsDB        *uploads.Database
	cryptoDB         *crypto.Database
	progtodoDB       *progtodo.Database
	shortlinks       *shortlinks.Store
	githubFetcher    *github.Fetcher
	templates        *template.Template
	changelogMu      sync.Mutex
	changelog        map[int]changelogEntry
	forexMu          sync.Mutex // guards the three forex fields below
	forexCache       map[string]float64
	forexUpdate      time.Time
	forexLastAttempt time.Time // set on every fetch attempt, even a failed one, to cool down retries

	cryptoChartCache map[string]cryptoChartCacheEntry
	cryptoChartMu    sync.Mutex

	forexChartCache map[string]cryptoChartCacheEntry
	forexChartMu    sync.Mutex

	catalogMu   sync.Mutex // guards the cached /api/logs/catalog bodies: [0] public, [1] admin
	catalogAt   [2]time.Time
	catalogBody [2][]byte

	weatherMu      sync.Mutex
	weatherFetchMu sync.Mutex // serialises upstream fetches; held only while fetching
	weatherCache   []byte
	weatherAt      time.Time
	weatherKey     string
	weatherFail    []byte // last failure payload, replayed for weatherRetryAfter
	weatherFailAt  time.Time
	weatherClient  *http.Client

	aiClient *ai.Client
	// rehashExt runs the same full apply as IRC/SIGHUP; fromWeb is true for any HTTP-originated reload
	// so listener stop/rebind can be deferred and avoid deadlock with the active request.
	rehashExt func(source string, fromWeb bool) error

	loginRateLimiter *LoginRateLimiter

	httpSvr   *http.Server
	httpSvrMu sync.Mutex

	done      chan struct{} // closed by Close; stops the server's background loops
	closeOnce sync.Once
}

func (s *Server) getConfig() *config.Config {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg
}

// SetConfig replaces the in-memory config after a live reload (shared pointer with IRC/RSS).
func (s *Server) SetConfig(cfg *config.Config) {
	s.cfgMu.Lock()
	s.cfg = ownConfig(cfg)
	s.cfgMu.Unlock()
	s.clearWeatherCache()
	s.catalogMu.Lock()
	s.catalogBody = [2][]byte{}
	s.catalogMu.Unlock()
}

// ownConfig returns a private copy of cfg for the web server. Handlers mutate s.cfg in
// place and then trigger a rehash; if s.cfg were the pointer the bot is running on, the
// rehash diff would compare the already-edited config with itself and miss every change
// (e.g. an edited IRC server/port never reconnected). Falls back to cfg if cloning fails.
func ownConfig(cfg *config.Config) *config.Config {
	if c, err := config.CloneConfig(cfg); err == nil {
		return c
	}
	return cfg
}

// NewServer creates a new web server instance. Call Close when it is discarded (web disabled by a
// rehash) so its database handles and background loops are released.
func NewServer(cfg *config.Config, bot *irc.Bot, rssFetcher *rss.Fetcher, statsTracker *stats.Tracker, bookmarksDB *bookmarks.Database, uploadsDB *uploads.Database, cryptoDB *crypto.Database, progtodoDB *progtodo.Database, githubFetcher *github.Fetcher, aiClient *ai.Client, rehashExt func(source string, fromWeb bool) error) (*Server, error) {
	tmpl, err := template.ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}

	authDB, err := NewAuthDatabase("data/web_auth.db")
	if err != nil {
		return nil, fmt.Errorf("initialize auth database: %w", err)
	}

	if err := authDB.CheckAndSeedInitialAdmin(cfg); err != nil {
		log.Printf("Warning: failed to seed initial admin: %v", err)
	}

	srv := &Server{
		cfg:              ownConfig(cfg),
		bot:              bot,
		rssFetcher:       rssFetcher,
		statsTracker:     statsTracker,
		bookmarksDB:      bookmarksDB,
		authDB:           authDB,
		uploadsDB:        uploadsDB,
		cryptoDB:         cryptoDB,
		progtodoDB:       progtodoDB,
		githubFetcher:    githubFetcher,
		aiClient:         aiClient,
		templates:        tmpl,
		rehashExt:        rehashExt,
		loginRateLimiter: NewLoginRateLimiter(15*time.Minute, 5),
		weatherClient: &http.Client{
			Timeout: 22 * time.Second,
		},
		done: make(chan struct{}),
	}
	srv.initShortLinks()
	guard.Go("web auth purge", srv.purgeAuthLoop)
	return srv, nil
}

// purgeAuthLoop deletes expired dashboard sessions and CSRF tokens (they were only dropped when
// the same token was presented again) until the server is closed.
func (s *Server) purgeAuthLoop() {
	for {
		if err := s.authDB.PurgeExpired(); err != nil {
			log.Printf("auth purge: %v", err)
		}
		select {
		case <-s.done:
			return
		case <-time.After(6 * time.Hour):
		}
	}
}

// Close releases what NewServer opened: background loops, the auth and short-link databases and
// the "self" shortener hook. Stop must have been called first; the Server is unusable afterwards.
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		if s.done != nil {
			close(s.done)
		}
		if s.loginRateLimiter != nil {
			s.loginRateLimiter.Close()
		}
		if s.shortlinks != nil {
			rss.SetSelfShortener(nil)
			s.shortlinks.Close()
		}
		if s.authDB != nil {
			s.authDB.Close()
		}
	})
}

// Open SSE streams allowed per client IP / in total (the dashboard itself uses one or two per tab).
const (
	maxStreamsPerIP = 6
	maxStreamsTotal = 64
)

// newServeMux builds the HTTP route table (reused on hot rebind after Shutdown).
func (s *Server) newServeMux() *http.ServeMux {
	mux := http.NewServeMux()

	// API endpoints
	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/logs/catalog", s.handleLogCatalog)
	mux.HandleFunc("/api/logs/history", s.handleLogHistory)
	streams := newConnLimiter(maxStreamsPerIP, maxStreamsTotal)
	mux.HandleFunc("/api/logs/stream", streams.wrap(s.handleLogStream, s.clientHostFromRequest))
	mux.HandleFunc("/api/rss/toggle", s.handleRSSToggle)
	mux.HandleFunc("/api/rss/news", s.handleRSSNews)
	mux.HandleFunc("/api/rss/fetch", s.handleRSSFetchNow)
	mux.HandleFunc("/api/rss/settings", s.handleRSSSettings)
	mux.HandleFunc("/api/github/toggle", s.handleGitHubToggle)
	mux.HandleFunc("/api/github/settings", s.handleGitHubTrackerSettings)
	mux.HandleFunc("/api/github/repos", s.handleGitHubTrackerRepos)
	mux.HandleFunc("/api/github/repos/edit", s.handleGitHubTrackerRepoEdit)
	mux.HandleFunc("/api/rehash", s.handleRehash)
	mux.HandleFunc("/api/irc/channels/reveal", s.handleIRCChannelReveal)
	mux.HandleFunc("/api/irc/channels/announce", s.handleIRCChannelAnnounce)
	mux.HandleFunc("/api/irc/channels/autojoin", s.handleIRCChannelAutojoin)
	mux.HandleFunc("/api/irc/channels/session", s.handleIRCChannelSession)
	mux.HandleFunc("/api/irc/channels", s.handleIRCChannels)
	mux.HandleFunc("/api/irc/networks/edit", s.handleIRCNetworkEdit)
	mux.HandleFunc("/api/irc/networks/nickserv", s.handleIRCNickServ)
	mux.HandleFunc("/api/irc/networks/cert", s.handleIRCNetworkCert)
	mux.HandleFunc("/api/irc/networks", s.handleIRCNetworks)
	mux.HandleFunc("/api/config/irc-admins", s.handleConfigIRCAdmins)
	mux.HandleFunc("/api/irc/access", s.handleIRCAccess)
	mux.HandleFunc("/api/stats/stream", streams.wrap(s.handleStatsStream, s.clientHostFromRequest))
	mux.HandleFunc("/api/stats/toggle", s.handleStatsToggle)
	mux.HandleFunc("/api/stats/history", s.handleStatsHistory)
	mux.HandleFunc("/api/chanstats", s.handleChanStats)
	mux.HandleFunc("/api/bookmarks", s.handleBookmarks)
	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/api/logout", s.handleLogout)
	mux.HandleFunc("/api/csrf-token", s.handleCSRFToken)
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
	mux.HandleFunc("/api/programmer-todos", s.handleProgrammerTodos)
	mux.HandleFunc("/api/weather", s.handleWeather)
	mux.HandleFunc("/api/weather/settings", s.handleWeatherSettings)
	mux.HandleFunc("/api/logger/settings", s.handleLoggerSettings)
	mux.HandleFunc("/api/ai/settings", s.handleAISettings)
	mux.HandleFunc("/api/changelog", s.handleChangelog)
	mux.HandleFunc("/favicon.ico", s.handleFavicon)

	// Upload/Paste routes
	mux.HandleFunc("/upload", s.handleUpload)
	mux.HandleFunc("/upload/cancel", s.handleUploadCancel)
	mux.HandleFunc("/r/", s.handleShortRedirect)
	mux.HandleFunc("/f/", s.handleFileDownload)
	mux.HandleFunc("/p/", s.handlePasteView)

	// Static files (app.js)
	mux.HandleFunc("/static/", s.handleStatic)

	// Dashboard page
	mux.HandleFunc("/", s.handleDashboard)
	return mux
}

// Start runs the web listener in a goroutine. It is a no-op if already listening.
func (s *Server) Start() error {
	s.httpSvrMu.Lock()
	if s.httpSvr != nil {
		s.httpSvrMu.Unlock()
		return nil
	}
	cfg := s.getConfig()
	addr := fmt.Sprintf("%s:%d", cfg.Web.Host, cfg.Web.Port)
	mux := s.newServeMux()
	// No WriteTimeout: /api/logs/stream is a long-lived SSE response.
	srv := &http.Server{
		Addr:              addr,
		Handler:           secure(mux),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	s.httpSvr = srv
	s.httpSvrMu.Unlock()

	log.Printf("Starting web dashboard on http://%s", addr)
	fmt.Printf("\n--------------------------------------------------\n")
	fmt.Printf("🚀 Web Dashboard: http://%s\n", addr)
	fmt.Printf("--------------------------------------------------\n\n")

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("web server: %v", err)
		}
	}()
	return nil
}

// Stop gracefully shuts down the HTTP server. No-op if not listening. Safe to call multiple times.
func (s *Server) Stop(ctx context.Context) error {
	s.httpSvrMu.Lock()
	srv := s.httpSvr
	s.httpSvr = nil
	s.httpSvrMu.Unlock()
	if srv == nil {
		return nil
	}
	if err := srv.Shutdown(ctx); err != nil {
		_ = srv.Close()
		return err
	}
	return nil
}

// handleHealth returns a simple health check response for load balancers and monitoring.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	status := map[string]interface{}{
		"status":         "healthy",
		"version":        meta.Version,
		"uptime_seconds": int(time.Since(s.bot.GetStartTime()).Seconds()),
	}

	// Check database connectivity
	if s.cryptoDB != nil {
		if prices, err := s.cryptoDB.GetLatestPrices(); err == nil && len(prices) > 0 {
			status["db_status"] = "ok"
		} else {
			status["db_status"] = "degraded"
		}
	}

	// Check IRC connection. irc_connected/status stay "ANY network up" for existing
	// consumers; connected_networks/total_networks let a network-aware client (or a human
	// reading this endpoint) see a partial outage instead of it reading as fully healthy.
	netStatuses := s.bot.NetworkStatuses()
	connectedNets := 0
	for _, st := range netStatuses {
		if st.Connected {
			connectedNets++
		}
	}
	status["connected_networks"] = connectedNets
	status["total_networks"] = len(netStatuses)
	if connectedNets == 0 {
		status["status"] = "degraded"
		status["irc_connected"] = false
	} else {
		status["irc_connected"] = true
		if connectedNets < len(netStatuses) {
			status["status"] = "degraded"
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
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

	interval := s.getConfig().Stats.Interval
	if interval <= 0 {
		interval = 60
	}
	weatherMin := s.getConfig().Web.WeatherRefreshMinutes
	if weatherMin < 1 {
		weatherMin = 1
	}
	// connected_networks/total_networks (and the authenticated pair below) let the
	// dashboard badges degrade for a partial outage instead of reading fully healthy off
	// "connected"/"irc_authenticated", which are ANY-network booleans kept for back-compat.
	netStatuses := s.bot.NetworkStatuses()
	connectedNets, authenticatedNets := 0, 0
	for _, st := range netStatuses {
		if st.Connected {
			connectedNets++
		}
		if st.Authenticated {
			authenticatedNets++
		}
	}
	status := map[string]interface{}{
		"version":                 meta.Version,
		"uptime":                  s.bot.GetUptime(),
		"connected":               s.bot.IsConnected(),
		"connected_networks":      connectedNets,
		"total_networks":          len(netStatuses),
		"authenticated_networks":  authenticatedNets,
		"networks":                netStatuses,
		"ai_model":                s.getConfig().AI.Model,
		"ai_status":               "Online",
		"ai_requests":             s.bot.GetAIRequestCount(),
		"rss_enabled":             s.rssFetcher.IsEnabled(),
		"stats_enabled":           s.statsTracker.IsEnabled(),
		"github_enabled":          s.githubFetcher.IsEnabled(),
		"stats_interval_seconds":  interval,
		"weather_refresh_minutes": weatherMin,
		"start_time":              s.bot.GetStartTime().Format(time.RFC3339),
		"is_admin":                sessionOK,
		"staff_admin":             staffAdmin,
		"needs_password_change":   needsChange,
		"irc_authenticated":       s.bot.IsAuthenticated(),
		"pending_pastes":          pendingPastes,
		"pending_uploads":         pendingUploads,
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

func (s *Server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><rect fill="#0284c7" width="24" height="24"/><path fill="#fff" d="M12 2L2 7l10 5 10-5-10-5zM2 17l10 5 10-5M2 12l10 5 10-5"/></svg>`))
}

type staticAsset struct {
	contentType string
	body        []byte
	etag        string
}

// staticAssets are the embedded dashboard files, read and hashed once.
var staticAssets = sync.OnceValue(func() map[string]staticAsset {
	files := map[string]string{
		"/static/app.js":    "application/javascript; charset=utf-8",
		"/static/style.css": "text/css; charset=utf-8",
	}
	out := make(map[string]staticAsset, len(files))
	for path, ctype := range files {
		data, err := templatesFS.ReadFile("templates/" + strings.TrimPrefix(path, "/static/"))
		if err != nil {
			continue
		}
		sum := sha256.Sum256(data)
		out[path] = staticAsset{contentType: ctype, body: data, etag: `"` + hex.EncodeToString(sum[:8]) + `"`}
	}
	return out
})

// handleStatic serves app.js / style.css (~250 KB together) with an ETag, so a reload costs a 304
// instead of re-sending them; no-cache keeps a new release visible immediately.
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	a, ok := staticAssets()[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("ETag", a.etag)
	w.Header().Set("Cache-Control", "no-cache")
	if r.Header.Get("If-None-Match") == a.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", a.contentType)
	w.Write(a.body)
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

func (s *Server) handleRehash(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireAdmin(w, r) {
		return
	}
	if s.rehashExt == nil {
		http.Error(w, "Rehash unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := s.rehashExt("web admin", true); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// runFullRehashFromWeb applies the same reload as !rehash / SIGHUP (AI, diff NOTICE, web hot, etc.).
func (s *Server) runFullRehashFromWeb(source string) error {
	if s.rehashExt == nil {
		return fmt.Errorf("rehash not configured")
	}
	return s.rehashExt(source, true)
}

func (s *Server) clientHostFromRequest(r *http.Request) string {
	return GetClientIP(r, s.getConfig().Web.TrustForwardedFor)
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

func generateHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
