package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSaveConfig_RejectsInvalid guards the config-bricking bug: a runtime write (e.g. an
// admin's !join creating a channel name already used by another network) must be refused
// before it reaches disk, not silently written and only caught on the next LoadConfig/start.
func TestSaveConfig_RejectsInvalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cfg := &Config{IRC: IRCConfig{Networks: []IRCNetworkConfig{
		{Name: "alpha", Server: "a.example", Channels: []IRChannel{{Name: "#dup"}}},
		{Name: "beta", Server: "b.example", Channels: []IRChannel{{Name: "#dup"}}},
	}}}

	err := SaveConfig(path, cfg)
	if err == nil {
		t.Fatal("expected SaveConfig to reject a config with the same channel on two networks")
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Fatal("SaveConfig must not write the file when validation fails")
	}
}

// TestSaveConfig_AtomicWrite confirms a successful save doesn't leave a stray .tmp file and
// that the target file round-trips.
func TestSaveConfig_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cfg := &Config{IRC: IRCConfig{Networks: []IRCNetworkConfig{
		{Name: "alpha", Server: "a.example", Port: 6697, Nickname: "bot"},
	}}}

	if err := SaveConfig(path, cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config file not written: %v", err)
	}
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Fatal("stray .tmp file left behind after a successful save")
	}

	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig after save: %v", err)
	}
	if len(loaded.IRC.Networks) != 1 || loaded.IRC.Networks[0].Name != "alpha" {
		t.Fatalf("round-trip mismatch: %+v", loaded.IRC.Networks)
	}
}
