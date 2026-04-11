package main

import (
	"fmt"
	"log"

	"botIAask/ai"
	"botIAask/config"
	"botIAask/irc"
)

func main() {
	// Path to the configuration file
	configPath := "config/config.yaml"

	// Load configuration
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	fmt.Printf("Starting Bot with config from: %s\n", configPath)
	fmt.Printf("IRC Server: %s:%d (SSL: %v)\n", cfg.IRC.Server, cfg.IRC.Port, cfg.IRC.UseSSL)
	fmt.Printf("AI Endpoint: %s\n", cfg.AI.LMStudioURL)

	// Initialize AI Client
	aiClient := ai.NewClient(cfg.AI.LMStudioURL, cfg.AI.Model)

	// Initialize IRC Bot
	bot := irc.NewBot(cfg, aiClient)

	// Start the bot
	fmt.Println("Connecting to IRC...")
	err = bot.Start()
	if err != nil {
		log.Fatalf("Bot error: %v", err)
	}
}
