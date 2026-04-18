package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"botIAask/ai"
	"botIAask/config"
	"botIAask/irc"
)

func main() {
	// Command-line flags
	daemon := flag.Bool("daemon", false, "Run bot in daemon mode")
	debug := flag.Bool("debug", true, "Enable debug mode with console output")
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

	// Handle mode flags (start, stop, restart) - these functions are defined in daemon.go
	if *mode != "" {
		switch *mode {
		case "start":
			err := StartDaemon()
			if err != nil {
				log.Fatalf("Failed to start daemon: %v", err)
			}
			return
		case "stop":
			err := StopDaemon()
			if err != nil {
				log.Fatalf("Failed to stop daemon: %v", err)
			}
			return
		case "restart":
			err := RestartDaemon()
			if err != nil {
				log.Fatalf("Failed to restart daemon: %v", err)
			}
			return
		}
	}

	// Path to the configuration file
	configPath := "config/config.yaml"

	// Load configuration
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// CLI flag overrides config file.
	if *debug {
		cfg.Bot.Debug = true
	}

	if cfg.Bot.Debug {
		fmt.Printf("Starting Bot with config from: %s\n", configPath)
		fmt.Printf("IRC Server: %s:%d (SSL: %v)\n", cfg.IRC.Server, cfg.IRC.Port, cfg.IRC.UseSSL)
		fmt.Printf("Endpoint: %s\n", cfg.AI.LMStudioURL)
	}

	// Initialize AI Client
	aiClient := ai.NewClient(cfg.AI.LMStudioURL, cfg.AI.Model)

	// Initialize IRC Bot
	bot := irc.NewBot(cfg, aiClient)

	// Handle daemon mode
	if *daemon {
		// Run in daemon mode
		err := runAsDaemon(cfg, bot, aiClient)
		if err != nil {
			log.Fatalf("Failed to start daemon: %v", err)
		}
	} else {
		// Run in foreground with debug mode
		runInForeground(cfg, bot, aiClient)
	}
}

func runAsDaemon(cfg *config.Config, bot *irc.Bot, aiClient *ai.Client) error {
	// Create PID file
	pid := os.Getpid()
	pidFile := "bot.pid"
	err := createPIDFile(pidFile, pid)
	if err != nil {
		return fmt.Errorf("failed to create PID file: %w", err)
	}
	defer func() {
		// Clean up PID file on exit
		os.Remove(pidFile)
	}()

	// Start the web server if requested or configured
	if cfg.Web.Enabled {
		go startWebServer(cfg, bot)
	}

	// Start the IRC bot
	if cfg.Bot.Debug {
		fmt.Println("Connecting to IRC (daemon mode)...")
	}

	// Run the bot in a goroutine so we can handle signals
	go func() {
		err := bot.Start()
		if err != nil {
			log.Printf("Bot error: %v", err)
		}
	}()

	// Wait indefinitely for signals or shutdown
	// This keeps the daemon process alive
	// We need to handle signal properly in daemon mode
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c

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
	sig := <-c
	log.Printf("Received signal: %v. Shutting down gracefully...", sig)

	// Give some time for graceful shutdown
	time.Sleep(1 * time.Second)
}

func forkProcess() error {
	// Fork the process using syscall
	// This is a simplified approach - in production, you might want to use a more robust solution
	// For now, we'll just detach from terminal by redirecting std streams

	// Create a new process group
	syscall.Setpgid(0, 0)

	return nil
}

func createPIDFile(filename string, pid int) error {
	absPath, err := filepath.Abs(filename)
	if err != nil {
		return fmt.Errorf("failed to get absolute path: %w", err)
	}

	file, err := os.Create(absPath)
	if err != nil {
		return fmt.Errorf("failed to create PID file: %w", err)
	}
	defer file.Close()

	_, err = file.WriteString(strconv.Itoa(pid))
	if err != nil {
		return fmt.Errorf("failed to write PID to file: %w", err)
	}

	return nil
}

func startWebServer(cfg *config.Config, bot *irc.Bot) {
	// Web server implementation
	log.Println("Starting web server on", cfg.Web.Host, ":", cfg.Web.Port)

	// For now, we'll just log that the web server would start here.
	// In a real implementation, we would set up an HTTP server with routes for:
	// - Bot status information
	// - Command history
	// - Configuration management
	// - Real-time updates via WebSockets

	// Example of what would be implemented:
	/*
		router := mux.NewRouter()
		router.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
			// Return bot status information
			status := map[string]interface{}{
				"uptime": time.Since(startTime).String(),
				"connected": bot.IsConnected(),
				"channels": cfg.IRC.Channels,
				"nickname": cfg.IRC.Nickname,
			}
			json.NewEncoder(w).Encode(status)
		}).Methods("GET")

		server := &http.Server{
			Addr:    fmt.Sprintf("%s:%d", cfg.Web.Host, cfg.Web.Port),
			Handler: router,
		}

		log.Fatal(server.ListenAndServe())
	*/
}
