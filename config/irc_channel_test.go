package config

import (
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestIRChannelYAMLRoundTrip(t *testing.T) {
	const in = `irc:
  server: irc.example.com
  port: 6697
  use_ssl: true
  nickname: Bot
  channels:
    - '#public'
    - name: '##private'
      password: 'sekrit'
  services:
    enabled: false
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(in), &cfg); err != nil {
		t.Fatal(err)
	}
	if got := len(cfg.IRC.Channels); got != 2 {
		t.Fatalf("channels: got %d want 2", got)
	}
	if cfg.IRC.Channels[0].Name != "#public" || cfg.IRC.Channels[0].Password != "" {
		t.Fatalf("public: %+v", cfg.IRC.Channels[0])
	}
	if cfg.IRC.Channels[1].Name != "##private" || cfg.IRC.Channels[1].Password != "sekrit" {
		t.Fatalf("private: %+v", cfg.IRC.Channels[1])
	}
	out, err := yaml.Marshal(&cfg.IRC)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "'##private'") {
		t.Logf("marshaled irc:\n%s", out)
		t.Fatal("expected keyed channel in marshaled yaml")
	}
}

func TestLoadConfigTemplate(t *testing.T) {
	path := filepath.Join("..", "config", "config.yaml.template")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.IRC.Channels) < 1 {
		t.Fatalf("expected at least one irc channel in template, got %d", len(cfg.IRC.Channels))
	}
}
