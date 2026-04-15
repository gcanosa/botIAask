package main

import (
	"flag"
	"fmt"
	"log"

	"botIAask/ai"
	"botIAask/config"
	"botIAask/irc"
)

func main() {
	// Command-line flag for web server
	webServer := flag.Bool("web-server", false, "Start the web dashboard server")
	flag.Parse()

	// Path to the configuration file
	configPath := "config/config.yaml"

	// Load configuration
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
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

	// Start the web server if requested or configured
	if *webServer || cfg.Web.Enabled {
		go startWebServer(cfg, bot)
	}

	// Start the IRC bot
	if cfg.Bot.Debug {
		fmt.Println("Connecting to IRC...")
	}
	err = bot.Start()
	if err != nil {
		log.Fatalf("Bot error: %v", err)
	}
}

func startWebServer(cfg *config.Config, bot *irc.Bot) {
	// TODO: Implement the web server logic here
	// This will be implemented in subsequent steps
	log.Println("Web server would start here")
}
