package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"botIAask/config"
)

func newEditServer(t *testing.T) *Server {
	t.Helper()
	t.Chdir(t.TempDir())
	if err := os.MkdirAll("config", 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.IRC.Networks = []config.IRCNetworkConfig{{Name: "libera", Server: "irc.libera.chat", Port: 6697, Nickname: "bot"}}
	cfg.Web.WeatherRefreshMinutes = 30
	return &Server{cfg: cfg}
}

// A rejected or unsaveable edit must leave the in-memory config exactly as it was (same pointer,
// because fetchers share it), instead of poisoning the next unrelated save.
func TestMutateConfigRollsBack(t *testing.T) {
	s := newEditServer(t)
	orig := s.cfg

	// 1. fn rejects after touching the config
	rec := httptest.NewRecorder()
	ok := s.mutateConfig(rec, func(c *config.Config) (int, error) {
		c.Web.WeatherRefreshMinutes = 99
		c.IRC.Networks[0].Nickname = "hijacked"
		return http.StatusBadRequest, errors.New("nope")
	})
	if ok || rec.Code != http.StatusBadRequest || rec.Body.String() != "nope\n" {
		t.Fatalf("rejected edit: ok=%v code=%d body=%q", ok, rec.Code, rec.Body.String())
	}
	if s.cfg != orig || s.cfg.Web.WeatherRefreshMinutes != 30 || s.cfg.IRC.Networks[0].Nickname != "bot" {
		t.Fatalf("rejected edit leaked into the config: %+v", s.cfg.Web)
	}

	// 2. fn accepts but SaveConfig refuses (two networks with the same name)
	rec = httptest.NewRecorder()
	ok = s.mutateConfig(rec, func(c *config.Config) (int, error) {
		c.IRC.Networks = append(c.IRC.Networks, config.IRCNetworkConfig{Name: "LIBERA", Server: "x", Port: 1, Nickname: "b"})
		return 0, nil
	})
	if ok || rec.Code != http.StatusInternalServerError {
		t.Fatalf("invalid config saved: ok=%v code=%d", ok, rec.Code)
	}
	if s.cfg != orig || len(s.cfg.IRC.Networks) != 1 {
		t.Fatal("unsaveable edit stayed in memory")
	}
	if _, err := os.Stat(config.DefaultConfigPath); err == nil {
		t.Fatal("a config file was written for an invalid edit")
	}

	// 3. a good edit is applied and persisted
	rec = httptest.NewRecorder()
	ok = s.mutateConfig(rec, func(c *config.Config) (int, error) {
		c.Web.WeatherRefreshMinutes = 45
		return 0, nil
	})
	if !ok || s.cfg.Web.WeatherRefreshMinutes != 45 {
		t.Fatalf("good edit: ok=%v minutes=%d body=%q", ok, s.cfg.Web.WeatherRefreshMinutes, rec.Body.String())
	}
	saved, err := config.LoadConfig(config.DefaultConfigPath)
	if err != nil || saved.Web.WeatherRefreshMinutes != 45 {
		t.Fatalf("not persisted: %v %+v", err, saved)
	}
}

func TestAdminListTargets(t *testing.T) {
	cfg := &config.Config{}
	cfg.Admin.Admins = []string{"g"}
	cfg.IRC.Networks = []config.IRCNetworkConfig{{Name: "libera", Admins: []string{"n"}}}
	if l, ok := adminList(cfg, ""); !ok || (*l)[0] != "g" {
		t.Fatal("global list")
	}
	if l, ok := adminList(cfg, "LIBERA"); !ok || (*l)[0] != "n" {
		t.Fatal("network list (case-insensitive)")
	}
	if _, ok := adminList(cfg, "nope"); ok {
		t.Fatal("unknown network accepted")
	}
}
