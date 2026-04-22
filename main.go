package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"botIAask/ai"
	"botIAask/bookmarks"
	"botIAask/config"
	"botIAask/crypto"
	"botIAask/irc"
	"botIAask/logger"
	"botIAask/meta"
	"botIAask/rss"
	"botIAask/stats"
	"botIAask/uploads"
	"botIAask/web"
)

func main() {
	// Command-line flags
	daemon := flag.Bool("daemon", false, "Run bot in daemon mode")
	debug := flag.Bool("debug", true, "Enable debug mode with console output")
	dashboard := flag.Bool("dashboard", false, "Run in daemon mode and enable web dashboard")
	mode := flag.String("mode", "", "Operation mode: start, stop, restart, or empty for foreground")
	version := flag.Bool("version", false, "Show version information")
	about := flag.Bool("about", false, "Show about information")
	news := flag.Bool("news", false, "Enable RSS news fetcher")
	updateNews := flag.Bool("updatenews", false, "Backfill RSS database (fetch last X items) and exit")
	dropNews := flag.Bool("dropnews", false, "Clear all news from the local database and exit")
	rehashCLI := flag.Bool("rehash", false, "Signal the running daemon (SIGHUP) to reload config/config.yaml")

	flag.Usage = func() {
		out := flag.CommandLine.Output()
		fmt.Fprintf(out, "%s v%s\n%s\n\n", meta.Name, meta.Version, meta.Author)
		fmt.Fprintf(out, "Usage of %s:\n", meta.Name)
		flag.PrintDefaults()
		fmt.Fprintf(flag.CommandLine.Output(), "\nIRC Commands (prefix configurable, default '!'):\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  !ask <query>     - Ask the AI a question\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  !news [limit]    - Fetch recent Hacker News\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  !uptime          - Show bot uptime\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  !spec            - Show system prompt spec\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  !paste           - Upload a text paste/log\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  !upload          - Request a link to upload a file (max size in web settings)\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  !download [N]    - List your approved file uploads with download URLs (newest first; optional last N)\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  !help            - Show this help message in IRC\n")
		fmt.Fprintf(flag.CommandLine.Output(), "\nIRC Admin Commands (require hostmask auth AND '!admin' session):\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  !admin           - Log in to admin session\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  !admin off       - Log out of admin session\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  !join #channel [key] - Join a channel, optional +k key (saved in config)\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  !part [#channel] - Leave a channel (updates config/config.yaml)\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  !ignore <nick>   - Ignore a user\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  !say #chan <msg> - Send a message to a channel\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  !news on/off     - Toggle news in current channel (session only)\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  !news start/stop - Turn RSS IRC announcements on/off (admin; saves config)\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  !stats           - View bot statistics\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  !op [nick]       - Give operator status to self or nick\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  !deop [nick]     - Remove operator status from self or nick\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  !voice [nick]    - Give voice status to self or nick\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  !devoice [nick]  - Remove voice status from self or nick\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  !ticket approve/cancel <ID> - Manage pending pastes\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  !rehash          - Reload config from disk (live; notifies admins)\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  !quit [reason]   - Quit (default message from irc.quit_message or name+version+uptime)\n")
		fmt.Fprintf(flag.CommandLine.Output(), "\nCLI:\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  -rehash          - Send SIGHUP to PID in daemon.pid (daemon mode only)\n")
	}
	flag.Parse()
	// Handle version and about flags
	if *version {
		fmt.Printf("%s v%s\n", meta.Name, meta.Version)
		return
	}

	if *about {
		fmt.Println("botIAask - An IRC bot powered by AI")
		fmt.Printf("Version: %s\n", meta.Version)
		fmt.Printf("Programmer: %s\n", meta.Author)
		fmt.Println("Features:")
		fmt.Println("  - AI-powered responses via LM Studio")
		fmt.Println("  - Rate limiting for commands")
		fmt.Println("  - Uptime tracking")
		fmt.Println("  - Command prefix support")
		fmt.Println("  - Daemon mode support")
		return
	}

	// Validate mode
	switch *mode {
	case "start", "stop", "restart", "":
	default:
		log.Fatalf("Invalid mode: %s. Must be 'start', 'stop', 'restart', or empty", *mode)
	}

	// Path to the configuration file
	configPath := "config/config.yaml"

	if _, err := os.Stat(configPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			log.Fatalf("configuration file %q not found. Copy config/config.yaml.template to config/config.yaml, edit it for your environment, then run again.", configPath)
		}
		log.Fatalf("configuration file %q: %v", configPath, err)
	}

	// Load configuration
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}
	if *rehashCLI {
		if err := signalDaemonRehash(cfg); err != nil {
			log.Fatalf("rehash: %v", err)
		}
		fmt.Println("SIGHUP sent; running bot will reload config and NOTICE logged-in IRC admins.")
		return
	}
	if cfg.Stats.Enabled && !cfg.Stats.SaveToDB {
		log.Printf("Warning: stats enabled but save_to_db is false; activity history will not persist across restarts")
	}

	// CLI flag overrides.
	if *debug {
		cfg.Bot.Debug = true
	}
	if *dashboard {
		*daemon = true
		cfg.Web.Enabled = true
		if cfg.Bot.Debug {
			fmt.Println("Dashboard flag active: enabling web server and forcing daemon mode.")
		}
	}
	if *news {
		cfg.RSS.Enabled = true
		*daemon = true // RSS fetcher usually runs in background
		if cfg.Bot.Debug {
			fmt.Println("News flag active: enabling RSS fetcher and forcing daemon mode.")
		}
	}

	isDaemonChild := os.Getenv("BOT_DAEMON_INTERNAL") == "1"
	effectiveMode := *mode
	if *daemon && effectiveMode == "" {
		effectiveMode = "start"
	}
	daemonParentWillSpawn := !isDaemonChild && (effectiveMode == "start" || effectiveMode == "restart") && (*mode != "" || *daemon)

	if cfg.Bot.Debug && !daemonParentWillSpawn {
		fmt.Printf("Starting Bot with config from: %s\n", configPath)
		fmt.Printf("IRC Server: %s:%d (SSL: %v)\n", cfg.IRC.Server, cfg.IRC.Port, cfg.IRC.UseSSL)
		fmt.Printf("Endpoint: %s\n", cfg.AI.LMStudioURL)
		if cfg.RSS.Enabled {
			fmt.Printf("RSS-Fetcher: ENABLED (Source: %s, Interval: %d min)\n", "https://news.ycombinator.com/rss", cfg.RSS.IntervalMinutes)
		}
	}

	// Ensure data directory exists
	if err := os.MkdirAll("data", 0755); err != nil {
		log.Fatalf("Failed to create data directory: %v", err)
	}

	// Handle mode flags (start, stop, restart) or the -daemon trigger
	if (*mode != "" || *daemon) && !isDaemonChild {
		switch effectiveMode {
		case "start":
			useColor := stdoutSupportsColor()
			printAppIdentity(os.Stdout, useColor)
			printDaemonParentReport(os.Stdout, cfg, configPath, useColor)
			err := StartDaemon(cfg)
			if err != nil {
				log.Fatalf("Failed to start daemon: %v", err)
			}
			return
		case "stop":
			err := StopDaemon(cfg)
			if err != nil {
				log.Fatalf("Failed to stop daemon: %v", err)
			}
			return
		case "restart":
			useColor := stdoutSupportsColor()
			printAppIdentity(os.Stdout, useColor)
			printDaemonParentReport(os.Stdout, cfg, configPath, useColor)
			err := RestartDaemon(cfg)
			if err != nil {
				log.Fatalf("Failed to restart daemon: %v", err)
			}
			return
		}
	}

	// Internal Initialization for maintenance flags
	if *dropNews {
		rssDB, err := rss.NewDatabase("data/rss_seen.db")
		if err != nil {
			log.Fatalf("Failed to initialize RSS database: %v", err)
		}
		fmt.Println("Dropping all news entries from database...")
		if err := rssDB.DropAll(); err != nil {
			log.Fatalf("Failed to drop news: %v", err)
		}
		fmt.Println("Done. Database cleared.")
		rssDB.Close()
		return
	}

	if *updateNews {
		rssDB, err := rss.NewDatabase("data/rss_seen.db")
		if err != nil {
			log.Fatalf("Failed to initialize RSS database: %v", err)
		}

		limit := 10
		if flag.NArg() > 0 {
			if val, err := strconv.Atoi(flag.Arg(0)); err == nil {
				limit = val
			}
		}

		fetcher := rss.NewFetcher(cfg, nil, rssDB)
		fmt.Printf("Backfilling RSS database with last %d items (syncing URLs)...\n", limit)
		count := fetcher.Backfill(limit)
		fmt.Printf("Done. Synchronized %d entries with URLs.\n", count)
		rssDB.Close()
		return
	}

	// Initialize AI Client
	aiClient := ai.NewClient(cfg.AI.LMStudioURL, cfg.AI.Model)

	// Initialize IRC Bot
	bot := irc.NewBot(cfg, aiClient)
	bot.SetConfigPath(configPath)

	// Initialize RSS Database
	rssDB, err := rss.NewDatabase("data/rss_seen.db")
	if err != nil {
		log.Fatalf("Failed to initialize RSS database: %v", err)
	}
	if err := rssDB.RepairEmptySourceHackerNewsWhenSingleHNFeed(cfg.RSS.FeedURLs); err != nil {
		log.Printf("RSS: repair source column: %v", err)
	}
	defer rssDB.Close()

	// Set database in bot for !news command
	bot.SetRSSDatabase(rssDB)

	// Initialize RSS Fetcher
	rssFetcher := rss.NewFetcher(cfg, bot, rssDB)
	if cfg.RSS.Enabled {
		go rssFetcher.Start()
	}

	var webServerMu sync.Mutex
	var webServerRef *web.Server

	// Initialize Stats Database
	statsDB, err := stats.NewDatabase("data/stats.db")
	if err != nil {
		log.Printf("Warning: Failed to initialize stats database: %v", err)
	} else {
		defer statsDB.Close()
	}

	// Initialize Stats Tracker
	statsTracker := stats.NewTracker(cfg, statsDB)
	go statsTracker.Start()
	bot.SetStatsTracker(statsTracker)

	rehashCoordinator := func(source string) error {
		newCfg, err := config.LoadConfig(configPath)
		if err != nil {
			return err
		}
		aiClient.UpdateConfig(newCfg.AI.LMStudioURL, newCfg.AI.Model)
		bot.ApplyLiveConfig(newCfg)
		rssFetcher.ApplyConfig(newCfg)
		statsTracker.ApplyConfig(newCfg)
		webServerMu.Lock()
		ws := webServerRef
		webServerMu.Unlock()
		if ws != nil {
			ws.SetConfig(newCfg)
		}
		if err := rssDB.RepairEmptySourceHackerNewsWhenSingleHNFeed(newCfg.RSS.FeedURLs); err != nil {
			log.Printf("RSS: repair source column after rehash: %v", err)
		}
		bot.NotifyLoggedInAdminsNotice(fmt.Sprintf("Config rehashed (%s) at %s", source, time.Now().Format(time.RFC3339)))
		log.Printf("Config rehash complete (%s)", source)
		return nil
	}
	bot.SetRehashHook(rehashCoordinator)

	// Initialize Bookmarks Database
	bookmarksDB, err := bookmarks.NewDatabase("data/bookmarks.db")
	if err != nil {
		log.Printf("Warning: Failed to initialize bookmarks database: %v", err)
	} else {
		defer bookmarksDB.Close()
		bot.SetBookmarksDatabase(bookmarksDB)
	}

	// Initialize Uploads Database (path relative to project root — same file for IRC + web even if cwd differs)
	uploadsDBPath, err := resolveUploadsDBPath(configPath, cfg.Uploads.DBPath)
	if err != nil {
		log.Fatalf("uploads db path: %v", err)
	}
	log.Printf("Uploads database: %s", uploadsDBPath)
	uploadsDB, err := uploads.NewDatabase(uploadsDBPath, "pastes", "upload_files")
	if err != nil {
		log.Printf("Warning: Failed to initialize uploads database: %v", err)
	} else {
		defer uploadsDB.Close()
		bot.SetUploadsDatabase(uploadsDB)
	}

	// Initialize Crypto Database
	var cryptoDB *crypto.Database
	cryptoDB, err = crypto.NewDatabase("data/crypto.db")
	if err != nil {
		log.Printf("Warning: Failed to initialize crypto database: %v", err)
	} else {
		defer cryptoDB.Close()
		bot.SetCryptoDatabase(cryptoDB)

		// Initialize Crypto Fetcher
		cryptoFetcher := crypto.NewFetcher(cryptoDB)
		go cryptoFetcher.Start()
	}

	// Start Log Rotator
	if cfg.Logger.RotationDays > 0 {
		logger.StartLogRotator(cfg.Logger.RotationDays)
	}

	// Handle daemon mode execution
	if *daemon || isDaemonChild {
		// Run in daemon mode (already detached if -mode start or -daemon was used)
		err := runAsDaemon(cfg, bot, aiClient, rssFetcher, statsTracker, bookmarksDB, uploadsDB, cryptoDB, rehashCoordinator, &webServerMu, &webServerRef)
		if err != nil {
			log.Fatalf("Failed to start daemon logic: %v", err)
		}
	} else {
		// Run in foreground with debug mode
		runInForeground(cfg, bot, aiClient, rssFetcher, statsTracker, bookmarksDB, uploadsDB, cryptoDB, rehashCoordinator, &webServerMu, &webServerRef)
	}
}

func runAsDaemon(cfg *config.Config, bot *irc.Bot, aiClient *ai.Client, rssFetcher *rss.Fetcher, statsTracker *stats.Tracker, bookmarksDB *bookmarks.Database, uploadsDB *uploads.Database, cryptoDB *crypto.Database, rehash func(string) error, webMu *sync.Mutex, webRef **web.Server) error {
	// Forked daemon child has stdio detached; avoid fmt to stdout (no terminal). Debug goes to log if configured.
	// Use configured PID file
	pidFile := cfg.Daemon.PIDFile
	err := WritePIDFile(pidFile)
	if err != nil {
		return fmt.Errorf("failed to create PID file: %w", err)
	}

	// Start the web server if requested or configured
	if cfg.Web.Enabled {
		go startWebServer(cfg, bot, rssFetcher, statsTracker, bookmarksDB, uploadsDB, cryptoDB, rehash, webMu, webRef)
	}

	// Start the IRC bot
	if cfg.Bot.Debug {
		fmt.Printf("Connecting to IRC (daemon mode, PID: %d)...\n", os.Getpid())
	}

	// Run the bot in a goroutine so we can handle signals
	go func() {
		err := bot.Start()
		if err != nil {
			log.Printf("Bot error: %v", err)
		}
	}()

	// Wait for signals or shutdown
	c := make(chan os.Signal, 2)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)

	for {
		sig := <-c
		if sig == syscall.SIGHUP {
			log.Println("SIGHUP received, reloading configuration...")
			if err := rehash("CLI (SIGHUP)"); err != nil {
				log.Printf("Failed to reload config: %v", err)
			}
			continue
		}

		if cfg.Bot.Debug {
			log.Printf("Daemon received signal: %v. Shutting down...", sig)
		}
		break
	}

	bot.RequestQuit("")

	// Clean up PID file on exit
	DeletePIDFile(pidFile)

	return nil
}

func runInForeground(cfg *config.Config, bot *irc.Bot, aiClient *ai.Client, rssFetcher *rss.Fetcher, statsTracker *stats.Tracker, bookmarksDB *bookmarks.Database, uploadsDB *uploads.Database, cryptoDB *crypto.Database, rehash func(string) error, webMu *sync.Mutex, webRef **web.Server) {
	// Set up signal handling for graceful shutdown
	c := make(chan os.Signal, 2)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)

	// Start the web server if requested or configured
	if cfg.Web.Enabled {
		go startWebServer(cfg, bot, rssFetcher, statsTracker, bookmarksDB, uploadsDB, cryptoDB, rehash, webMu, webRef)
	}

	// Start the IRC bot
	if cfg.Bot.Debug {
		fmt.Println("Connecting to IRC (foreground mode)...")
	}

	// Start the bot in a goroutine so we can handle signals
	go func() {
		err := bot.Start()
		if err != nil {
			log.Printf("Bot error: %v", err)
		}
	}()

	// Wait for signal
	for {
		sig := <-c
		if sig == syscall.SIGHUP {
			log.Println("SIGHUP received, reloading configuration...")
			if err := rehash("CLI (SIGHUP)"); err != nil {
				log.Printf("Failed to reload config: %v", err)
			}
			continue
		}
		log.Printf("Received signal: %v. Shutting down gracefully...", sig)
		break
	}

	bot.RequestQuit("")

	// Give some time for graceful shutdown
	time.Sleep(1 * time.Second)
}

func startWebServer(cfg *config.Config, bot *irc.Bot, rf *rss.Fetcher, st *stats.Tracker, bdb *bookmarks.Database, udb *uploads.Database, cdb *crypto.Database, rehash func(string) error, webMu *sync.Mutex, webRef **web.Server) {
	webRehash := func() error {
		return rehash("web admin")
	}
	ws := web.NewServer(cfg, bot, rf, st, bdb, udb, cdb, webRehash)
	webMu.Lock()
	*webRef = ws
	webMu.Unlock()
	if err := ws.Start(); err != nil {
		log.Printf("Web server error: %v", err)
	}
}

func signalDaemonRehash(cfg *config.Config) error {
	if !cfg.Daemon.Enabled {
		return fmt.Errorf("daemon is disabled in config; use !rehash in IRC or the web dashboard when not using a PID file")
	}
	pid, err := ReadPIDFile(cfg.Daemon.PIDFile)
	if err != nil {
		return fmt.Errorf("read pid file: %w", err)
	}
	if !IsProcessRunning(pid) {
		return fmt.Errorf("no running process for PID %d", pid)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Signal(syscall.SIGHUP)
}

// resolveUploadsDBPath resolves a relative db path against the project root (parent of the config directory),
// so IRC and web always share one DB when using default data/uploads.db.
func resolveUploadsDBPath(configPath, dbPath string) (string, error) {
	if dbPath == "" {
		dbPath = "data/uploads.db"
	}
	if filepath.IsAbs(dbPath) {
		return dbPath, nil
	}
	cfgAbs, err := filepath.Abs(configPath)
	if err != nil {
		return "", err
	}
	projectRoot := filepath.Dir(filepath.Dir(cfgAbs))
	return filepath.Join(projectRoot, dbPath), nil
}
