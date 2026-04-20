package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config represents the application configuration structure.
type Config struct {
	IRC         IRCConfig       `yaml:"irc"`
	AI          AIConfig        `yaml:"ai"`
	Bot         BotConfig       `yaml:"bot"`
	Admin       AdminConfig     `yaml:"admin"`
	Web         WebConfig       `yaml:"web,omitempty"`
	Daemon      DaemonConfig    `yaml:"daemon,omitempty"`
	RateLimiter RateLimitConfig `yaml:"rateLimiter,omitempty"`
	Logger      LoggerConfig    `yaml:"logger,omitempty"`
	RSS         RSSConfig       `yaml:"rss,omitempty"`
	Stats       StatsConfig     `yaml:"stats,omitempty"`
	Uploads     UploadsConfig   `yaml:"uploads,omitempty"`
}

// UploadsConfig holds limits for user file uploads (!upload flow).
type UploadsConfig struct {
	MaxFileMB int    `yaml:"max_file_mb"`             // default 200
	DBPath    string `yaml:"db_path,omitempty"`       // optional; relative paths resolve from project root (parent of config/); absolute path if multiple instances or odd cwd
}

// IRCConfig holds settings for the IRC connection.
type IRCConfig struct {
	Server   string         `yaml:"server"`
	Port     int            `yaml:"port"`
	UseSSL   bool           `yaml:"use_ssl"`
	Nickname string         `yaml:"nickname"`
	Channels []string       `yaml:"channels"`
	Services ServicesConfig `yaml:"services"`
}

// ServicesConfig holds settings for IRC services authentication (SASL).
type ServicesConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
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
	Limit   int  `yaml:"limit"`  // commands per minute
	Burst   int  `yaml:"burst"`  // allowance
	Window  int  `yaml:"window"` // window in seconds (default 60)
}

// AdminConfig holds settings for administrative users.
type AdminConfig struct {
	Admins []string `yaml:"admins"`
}

// DaemonConfig holds settings for daemon/background mode.
type DaemonConfig struct {
	Enabled bool   `yaml:"enabled"`
	PIDFile string `yaml:"pid_file"`
}

// WebConfig holds settings for the web dashboard server.
type WebConfig struct {
	Enabled bool       `yaml:"enabled"`
	Port    int        `yaml:"port"`
	Host    string     `yaml:"host"`
	BaseURL string     `yaml:"base_url"`
	Auth    AuthConfig `yaml:"auth,omitempty"`
}

// AuthConfig holds authentication settings for the web dashboard.
type AuthConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// LoggerConfig holds settings for log rotation.
type LoggerConfig struct {
	RotationDays int `yaml:"rotation_days"`
}

// RSSConfig holds settings for the Hacker News RSS fetcher.
type RSSConfig struct {
	Enabled         bool     `yaml:"enabled"`
	IntervalMinutes int      `yaml:"interval_minutes"`
	Channels        []string `yaml:"channels"`
	RetentionCount  int      `yaml:"retention_count"`
	FeedURLs        []string `yaml:"feed_urls"`
}

// StatsConfig holds settings for real-time statistics collection.
type StatsConfig struct {
	Enabled       bool `yaml:"enabled"`
	Interval      int  `yaml:"interval"` // in seconds
	SaveToDB      bool `yaml:"save_to_db"`
	RetentionDays int  `yaml:"retention_days"`
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

	applyStatsDefaults(&cfg)
	applyUploadsDefaults(&cfg)

	return &cfg, nil
}

func applyStatsDefaults(cfg *Config) {
	if cfg.Stats.Interval <= 0 {
		cfg.Stats.Interval = 60
	}
}

func applyUploadsDefaults(cfg *Config) {
	if cfg.Uploads.MaxFileMB <= 0 {
		cfg.Uploads.MaxFileMB = 200
	}
}

// SaveConfig writes the configuration to the specified YAML file.
func SaveConfig(path string, cfg *Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	err = os.WriteFile(path, data, 0644)
	if err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}
