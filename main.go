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
	"botIAask/internal/ircusage"
	"botIAask/irc"
	"botIAask/logger"
	"botIAask/meta"
	"botIAask/progtodo"
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
	showIRCUsage := flag.Bool("usage", false, "Show IRC command reference (user vs admin; use NO_COLOR=1 to disable color)")

	flag.Usage = func() {
		out := flag.CommandLine.Output()
		fmt.Fprintf(out, "%s v%s\n%s\n\n", meta.Name, meta.Version, meta.Author)
		fmt.Fprintf(out, "Usage of %s:\n", meta.Name)
		flag.PrintDefaults()
		fmt.Fprintln(out, "\nFor IRC command reference, run with -usage")
	}
	flag.Parse()
	// Handle version and about flags
	if *showIRCUsage {
		ircusage.Fprint(os.Stdout, ircusage.UseColorForStdout())
		return
	}
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
	configPath := config.DefaultConfigPath

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
	if cfg.Stats.Enabled && !cfg.Stats.ShouldSaveToDB() {
		log.Printf("Warning: stats enabled but save_to_db is false; activity history will not be stored in the stats database")
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

	if cfg.Bot.Debug && *mode == "" && !*daemon {
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
			err := StartDaemon(os.Stdout, cfg, useColor)
			if err != nil {
				log.Fatalf("Failed to start daemon: %v", err)
			}
			return
		case "stop":
			useColor := stdoutSupportsColor()
			printAppIdentity(os.Stdout, useColor)
			printDaemonParentReport(os.Stdout, cfg, configPath, useColor)
			err := StopDaemon(os.Stdout, cfg, useColor)
			if err != nil {
				log.Fatalf("Failed to stop daemon: %v", err)
			}
			return
		case "restart":
			useColor := stdoutSupportsColor()
			printAppIdentity(os.Stdout, useColor)
			printDaemonParentReport(os.Stdout, cfg, configPath, useColor)
			err := RestartDaemon(os.Stdout, cfg, useColor)
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

	// Initialize Bookmarks Database
	bookmarksDB, err := bookmarks.NewDatabase("data/bookmarks.db")
	if err != nil {
		log.Printf("Warning: Failed to initialize bookmarks database: %v", err)
	} else {
		defer bookmarksDB.Close()
		bot.SetBookmarksDatabase(bookmarksDB)
	}

	var progTodoDB *progtodo.Database
	progPath, perr := resolveDataFilePath(configPath, "data/prog_todos.db")
	if perr != nil {
		log.Fatalf("progtodo database path: %v", perr)
	}
	log.Printf("Programmer TODO database: %s", progPath)
	progTodoDB, err = progtodo.NewDatabase(progPath)
	if err != nil {
		log.Printf("Warning: Failed to initialize programmer TODO database: %v", err)
	} else {
		defer progTodoDB.Close()
		bot.SetProgtodoDatabase(progTodoDB)
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

	rstate := &rehashState{
		configPath:   configPath,
		aiClient:     aiClient,
		bot:          bot,
		rssFetcher:   rssFetcher,
		statsTracker: statsTracker,
		rssDB:        rssDB,
		webMu:        &webServerMu,
		webRef:       &webServerRef,
	}
	var applyRehash func(string, bool) error
	rstate.startWeb = func(cfg *config.Config) {
		startWebServer(cfg, bot, rssFetcher, statsTracker, bookmarksDB, uploadsDB, cryptoDB, progTodoDB, aiClient, applyRehash, &webServerMu, &webServerRef)
	}
	applyRehash = func(source string, fromWeb bool) error {
		return doApplyRehash(rstate, source, fromWeb)
	}
	bot.SetRehashHook(func(s string) error {
		return applyRehash(s, false)
	})

	// Start Log Rotator (loop reads live retention via SetRotationDays / rehash)
	logger.StartLogRotator(cfg.Logger.RotationDays)

	// Handle daemon mode execution
	if *daemon || isDaemonChild {
		// Run in daemon mode (already detached if -mode start or -daemon was used)
		err := runAsDaemon(cfg, bot, aiClient, rssFetcher, statsTracker, bookmarksDB, uploadsDB, cryptoDB, progTodoDB, applyRehash, &webServerMu, &webServerRef)
		if err != nil {
			log.Fatalf("Failed to start daemon logic: %v", err)
		}
	} else {
		// Run in foreground with debug mode
		runInForeground(cfg, bot, aiClient, rssFetcher, statsTracker, bookmarksDB, uploadsDB, cryptoDB, progTodoDB, applyRehash, &webServerMu, &webServerRef)
	}
}

func runAsDaemon(cfg *config.Config, bot *irc.Bot, aiClient *ai.Client, rssFetcher *rss.Fetcher, statsTracker *stats.Tracker, bookmarksDB *bookmarks.Database, uploadsDB *uploads.Database, cryptoDB *crypto.Database, progTodoDB *progtodo.Database, rehash func(string, bool) error, webMu *sync.Mutex, webRef **web.Server) error {
	// Forked daemon child has stdio detached; avoid fmt to stdout (no terminal). Debug goes to log if configured.
	// Use configured PID file
	pidFile := cfg.Daemon.PIDFile
	err := WritePIDFile(pidFile)
	if err != nil {
		return fmt.Errorf("failed to create PID file: %w", err)
	}

	// Start the web server if requested or configured
	if cfg.Web.Enabled {
		go startWebServer(cfg, bot, rssFetcher, statsTracker, bookmarksDB, uploadsDB, cryptoDB, progTodoDB, aiClient, rehash, webMu, webRef)
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
			if err := rehash("CLI (SIGHUP)", false); err != nil {
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

func runInForeground(cfg *config.Config, bot *irc.Bot, aiClient *ai.Client, rssFetcher *rss.Fetcher, statsTracker *stats.Tracker, bookmarksDB *bookmarks.Database, uploadsDB *uploads.Database, cryptoDB *crypto.Database, progTodoDB *progtodo.Database, rehash func(string, bool) error, webMu *sync.Mutex, webRef **web.Server) {
	// Set up signal handling for graceful shutdown
	c := make(chan os.Signal, 2)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)

	// Start the web server if requested or configured
	if cfg.Web.Enabled {
		go startWebServer(cfg, bot, rssFetcher, statsTracker, bookmarksDB, uploadsDB, cryptoDB, progTodoDB, aiClient, rehash, webMu, webRef)
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
			if err := rehash("CLI (SIGHUP)", false); err != nil {
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

func startWebServer(cfg *config.Config, bot *irc.Bot, rf *rss.Fetcher, st *stats.Tracker, bdb *bookmarks.Database, udb *uploads.Database, cdb *crypto.Database, tdb *progtodo.Database, aiClient *ai.Client, rehash func(string, bool) error, webMu *sync.Mutex, webRef **web.Server) {
	ws := web.NewServer(cfg, bot, rf, st, bdb, udb, cdb, tdb, aiClient, rehash)
	webMu.Lock()
	*webRef = ws
	webMu.Unlock()
	if err := ws.Start(); err != nil {
		log.Printf("Web server error: %v", err)
	}
}

func signalDaemonRehash(cfg *config.Config) error {
	if !cfg.Daemon.Enabled {
		return fmt.Errorf("daemon is disabled in config: enable [daemon] to use -rehash, or reload with `kill -HUP <pid>` on the running bot, `!rehash` in IRC, or POST /api/rehash on the dashboard")
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

// resolveDataFilePath resolves a path relative to the project root (parent of the config directory),
// e.g. data/prog_todos.db, so the DB is independent of the process current working directory.
func resolveDataFilePath(configPath, rel string) (string, error) {
	rel = filepath.Clean(rel)
	if rel == "" || rel == "." {
		return "", fmt.Errorf("empty relative path")
	}
	if filepath.IsAbs(rel) {
		return rel, nil
	}
	cfgAbs, err := filepath.Abs(configPath)
	if err != nil {
		return "", err
	}
	projectRoot := filepath.Dir(filepath.Dir(cfgAbs))
	return filepath.Join(projectRoot, rel), nil
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
