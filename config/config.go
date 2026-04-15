package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config represents the application configuration structure.
type Config struct {
	IRC   IRCConfig   `yaml:"irc"`
	AI    AIConfig    `yaml:"ai"`
	Bot   BotConfig   `yaml:"bot"`
	Admin AdminConfig `yaml:"admin"`
	Web   WebConfig   `yaml:"web,omitempty"`
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
	CommandPrefix string           `yaml:"command_prefix"`
	CommandName   string           `yaml:"command_name"`
	Debug         bool             `yaml:"debug"`
	RateLimiting  *RateLimitConfig `yaml:"rate_limiting,omitempty"`
}

// RateLimitConfig holds settings for rate limiting.
type RateLimitConfig struct {
	Enabled bool `yaml:"enabled"`
	Limit   int  `yaml:"limit"`      // commands per minute
	Burst   int  `yaml:"burst"`      // allowance
	Window  int  `yaml:"window"`     // window in seconds (default 60)
}

// AdminConfig holds settings for administrative users.
type AdminConfig struct {
	Admins []string `yaml:"admins"`
}

// WebConfig holds settings for the web dashboard server.
type WebConfig struct {
	Enabled bool     `yaml:"enabled"`
	Port    int      `yaml:"port"`
	Host    string   `yaml:"host"`
	Auth    AuthConfig `yaml:"auth,omitempty"`
}

// AuthConfig holds authentication settings for the web dashboard.
type AuthConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
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