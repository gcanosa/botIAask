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
	if len(cfg.IRC.Networks) != 1 {
		t.Fatalf("networks: got %d want 1", len(cfg.IRC.Networks))
	}
	chans := cfg.IRC.Networks[0].Channels
	if got := len(chans); got != 2 {
		t.Fatalf("channels: got %d want 2", got)
	}
	if chans[0].Name != "#public" || chans[0].Password != "" {
		t.Fatalf("public: %+v", chans[0])
	}
	if chans[1].Name != "##private" || chans[1].Password != "sekrit" {
		t.Fatalf("private: %+v", chans[1])
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
	if len(cfg.IRC.Networks) != 1 {
		t.Fatalf("networks: %d", len(cfg.IRC.Networks))
	}
	chans := cfg.IRC.Networks[0].Channels
	if got := len(chans); got != 1 {
		t.Fatalf("channels: %d", got)
	}
	if chans[0].AutoJoinEnabled() {
		t.Fatal("expected auto_join false")
	}
}

func TestLoadConfigTemplate(t *testing.T) {
	path := filepath.Join("..", "config", "config.yaml.template")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.IRC.Networks) < 1 {
		t.Fatalf("expected at least one irc network in template, got %d", len(cfg.IRC.Networks))
	}
	if len(cfg.IRC.Networks[0].Channels) < 1 {
		t.Fatalf("expected at least one irc channel in template, got %d", len(cfg.IRC.Networks[0].Channels))
	}
	// A fresh install must not ship a known dashboard password (the first admin gets a random one).
	if cfg.Web.Auth.Password != "" {
		t.Fatalf("template must not ship a default dashboard password, got %q", cfg.Web.Auth.Password)
	}
}

func TestIsKeyedChannel(t *testing.T) {
	cfg := &Config{IRC: IRCConfig{Networks: []IRCNetworkConfig{
		{Name: "a", Channels: []IRChannel{{Name: "#Staff", Password: "k"}, {Name: "#pub"}}},
		{Name: "b", Channels: []IRChannel{{Name: "#staff"}}},
	}}}
	if !IsKeyedChannel(cfg, "#staff", "a") || !IsKeyedChannel(cfg, "#STAFF", "") {
		t.Fatal("keyed channel not detected (case-insensitive, any network)")
	}
	if IsKeyedChannel(cfg, "#staff", "b") || IsKeyedChannel(cfg, "#pub", "a") {
		t.Fatal("unkeyed channel reported as keyed")
	}
}
