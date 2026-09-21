package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretsEncryptedOnDisk(t *testing.T) {
	d := t.TempDir()
	SecretKeyPath = filepath.Join(d, "data", "k.key")
	path := filepath.Join(d, "config.yaml")
	yml := "irc:\n  networks:\n    - name: n\n      server: s\n      port: 6697\n      nickname: b\n      channels:\n        - {name: '#a', password: chankey}\n      services: {enabled: true, username: u, password: hunter2}\nomdb: {api_key: omdbkey}\n"
	os.WriteFile(path, []byte(yml), 0600)
	cfg, err := LoadConfig(path) // migrates plaintext
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IRC.Networks[0].Services.Password != "hunter2" || cfg.OMDB.APIKey != "omdbkey" || cfg.IRC.Networks[0].Channels[0].Password != "chankey" {
		t.Fatalf("in-memory secrets must be plaintext: %+v", cfg)
	}
	raw, _ := os.ReadFile(path)
	for _, s := range []string{"hunter2", "omdbkey", "chankey"} {
		if strings.Contains(string(raw), s) {
			t.Fatalf("%q still plaintext on disk", s)
		}
	}
	cfg2, err := LoadConfig(path)
	if err != nil || cfg2.IRC.Networks[0].Services.Password != "hunter2" {
		t.Fatalf("roundtrip failed: %v", err)
	}
}

func TestNickServAndClientCertEncrypted(t *testing.T) {
	d := t.TempDir()
	SecretKeyPath = filepath.Join(d, "data", "k.key")
	path := filepath.Join(d, "config.yaml")
	bundle, fp, err := GenerateClientCert("bot")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ClientCertFingerprint(bundle); err != nil || got != fp {
		t.Fatalf("fingerprint mismatch: %v %q %q", err, got, fp)
	}
	cfg := &Config{IRC: IRCConfig{Networks: []IRCNetworkConfig{{
		Name: "n", Server: "s", Port: 6697, UseSSL: true, Nickname: "bot",
		Services: ServicesConfig{Enabled: true, Mechanism: "external", NickServPassword: "nspw", ClientCert: bundle},
	}}}}
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "nspw") || strings.Contains(string(raw), "PRIVATE KEY") {
		t.Fatal("secret in plaintext on disk")
	}
	if cfg.IRC.Networks[0].Services.NickServPassword != "nspw" {
		t.Fatal("SaveConfig must not mutate the in-memory config")
	}
	got, err := LoadConfig(path)
	if err != nil || got.IRC.Networks[0].Services.ClientCert != bundle || got.IRC.Networks[0].Services.NickServPassword != "nspw" {
		t.Fatalf("roundtrip failed: %v", err)
	}
	cfg.IRC.Networks[0].Services.ClientCert = ""
	if ValidateConfig(cfg) == nil {
		t.Fatal("EXTERNAL without cert must be rejected")
	}
}
