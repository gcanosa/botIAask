package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config represents the application configuration structure.
type Config struct {
	IRC IRCConfig `yaml:"irc"`
	AI  AIConfig  `yaml:"ai"`
	Bot BotConfig `yaml:"bot"`
}

// IRCConfig holds settings for the IRC connection.
type IRCConfig struct {
	Server   string   `yaml:"server"`
	Port     int      `yaml:"port"`
	UseSSL   bool     `yaml:"use_ssl"`
	Nickname string   `yaml:"nickname"`
	Channels []string `yaml:"channels"`
}

// AIConfig holds settings for the LM Studio connection.
type AIConfig struct {
	LMStudioURL string `yaml:"lm_studio_url"`
	Model       string `yaml:"model"`
}

// BotConfig holds settings for the bot's behavior.
type BotConfig struct {
	CommandPrefix string `yaml:"command_prefix"`
	CommandName   string `yaml:"command_name"`
}

// LoadConfig reads and parses the YAML configuration file.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	err = yaml.Unmarshal(data, &cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	return &cfg, nil
}
