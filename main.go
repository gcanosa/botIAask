package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"botIAask/ai"
	"botIAask/config"
	"botIAask/irc"
	"botIAask/logger"
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
	flag.Parse()

	// Handle version and about flags
	if *version {
		fmt.Println("botIAask v1.0.0")
		return
	}

	if *about {
		fmt.Println("botIAask - An IRC bot powered by AI")
		fmt.Println("Version: 1.0.0")
		fmt.Println("Programmer: Gerardo Canosa (gera.canosa@gmail.com)")
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

	// Load configuration
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
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

	if cfg.Bot.Debug {
		fmt.Printf("Starting Bot with config from: %s\n", configPath)
		fmt.Printf("IRC Server: %s:%d (SSL: %v)\n", cfg.IRC.Server, cfg.IRC.Port, cfg.IRC.UseSSL)
		fmt.Printf("Endpoint: %s\n", cfg.AI.LMStudioURL)
	}

	// Determine if this is an internal daemon process spawned by us
	isDaemonChild := os.Getenv("BOT_DAEMON_INTERNAL") == "1"

	// Handle mode flags (start, stop, restart) or the -daemon trigger
	if (*mode != "" || *daemon) && !isDaemonChild {
		effectiveMode := *mode
		if *daemon && effectiveMode == "" {
			effectiveMode = "start"
		}

		switch effectiveMode {
		case "start":
			if *dashboard {
				addr := fmt.Sprintf("%s:%d", cfg.Web.Host, cfg.Web.Port)
				fmt.Printf("\n--------------------------------------------------\n")
				fmt.Printf("🚀 Web Dashboard Service: http://%s\n", addr)
				fmt.Printf("--------------------------------------------------\n\n")
			}
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
			if *dashboard {
				addr := fmt.Sprintf("%s:%d", cfg.Web.Host, cfg.Web.Port)
				fmt.Printf("\n--------------------------------------------------\n")
				fmt.Printf("🚀 Web Dashboard Service: http://%s\n", addr)
				fmt.Printf("--------------------------------------------------\n\n")
			}
			err := RestartDaemon(cfg)
			if err != nil {
				log.Fatalf("Failed to restart daemon: %v", err)
			}
			return
		}
	}

	// Initialize AI Client
	aiClient := ai.NewClient(cfg.AI.LMStudioURL, cfg.AI.Model)

	// Initialize IRC Bot
	bot := irc.NewBot(cfg, aiClient)

	// Start Log Rotator
	if cfg.Logger.RotationDays > 0 {
		logger.StartLogRotator(cfg.Logger.RotationDays)
	}

	// Handle daemon mode execution
	if *daemon || isDaemonChild {
		// Run in daemon mode (already detached if -mode start or -daemon was used)
		err := runAsDaemon(cfg, bot, aiClient)
		if err != nil {
			log.Fatalf("Failed to start daemon logic: %v", err)
		}
	} else {
		// Run in foreground with debug mode
		runInForeground(cfg, bot, aiClient)
	}
}

func runAsDaemon(cfg *config.Config, bot *irc.Bot, aiClient *ai.Client) error {
	// Use configured PID file
	pidFile := cfg.Daemon.PIDFile
	err := WritePIDFile(pidFile)
	if err != nil {
		return fmt.Errorf("failed to create PID file: %w", err)
	}

	// Start the web server if requested or configured
	if cfg.Web.Enabled {
		go startWebServer(cfg, bot)
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
			newCfg, err := config.LoadConfig("config/config.yaml")
			if err != nil {
				log.Printf("Failed to reload config: %v", err)
				continue
			}
			bot.Reload(newCfg)
			continue
		}

		if cfg.Bot.Debug {
			log.Printf("Daemon received signal: %v. Shutting down...", sig)
		}
		break
	}

	// Clean up PID file on exit
	DeletePIDFile(pidFile)

	return nil
}

func runInForeground(cfg *config.Config, bot *irc.Bot, aiClient *ai.Client) {
	// Set up signal handling for graceful shutdown
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	// Start the web server if requested or configured
	if cfg.Web.Enabled {
		go startWebServer(cfg, bot)
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
			newCfg, err := config.LoadConfig("config/config.yaml")
			if err != nil {
				log.Printf("Failed to reload config: %v", err)
				continue
			}
			bot.Reload(newCfg)
			continue
		}
		log.Printf("Received signal: %v. Shutting down gracefully...", sig)
		break
	}

	// Give some time for graceful shutdown
	time.Sleep(1 * time.Second)
}

func startWebServer(cfg *config.Config, bot *irc.Bot) {
	ws := web.NewServer(cfg, bot)
	if err := ws.Start(); err != nil {
		log.Printf("Web server error: %v", err)
	}
}
