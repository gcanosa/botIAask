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

func TestIRChannelAutoJoinFalseYAML(t *testing.T) {
	f := false
	c := IRChannel{Name: "#chan", AutoJoin: &f}
	v, err := c.MarshalYAML()
	if err != nil {
		t.Fatal(err)
	}
	m, ok := v.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map, got %T", v)
	}
	if m["auto_join"] != false {
		t.Fatalf("auto_join: %+v", m)
	}
}

func TestIRChannelUnmarshalAutoJoin(t *testing.T) {
	const in = `irc:
  server: irc.example.com
  port: 6697
  use_ssl: true
  nickname: Bot
  channels:
    - name: '#nope'
      auto_join: false
  services:
    enabled: false
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(in), &cfg); err != nil {
		t.Fatal(err)
	}
	if got := len(cfg.IRC.Channels); got != 1 {
		t.Fatalf("channels: %d", got)
	}
	if cfg.IRC.Channels[0].AutoJoinEnabled() {
		t.Fatal("expected auto_join false")
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
