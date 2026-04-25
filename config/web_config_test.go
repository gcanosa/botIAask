package config

import (
	"testing"
)

func TestApplyWebDefaults_ServerLocation(t *testing.T) {
	cfg := &Config{}
	applyWebDefaults(cfg)
	if cfg.Web.ServerLocation != "Barcelona, Spain" {
		t.Fatalf("expected default server_location, got %q", cfg.Web.ServerLocation)
	}
	if cfg.Web.WeatherRefreshMinutes != 30 {
		t.Fatalf("expected default weather refresh 30, got %d", cfg.Web.WeatherRefreshMinutes)
	}
	cfg2 := &Config{Web: WebConfig{ServerLocation: "Tokyo"}}
	applyWebDefaults(cfg2)
	if cfg2.Web.ServerLocation != "Tokyo" {
		t.Fatalf("expected custom location preserved, got %q", cfg2.Web.ServerLocation)
	}
	if cfg2.Web.WeatherRefreshMinutes != 30 {
		t.Fatalf("expected default weather refresh 30, got %d", cfg2.Web.WeatherRefreshMinutes)
	}
	cfg3 := &Config{Web: WebConfig{WeatherRefreshMinutes: 5}}
	applyWebDefaults(cfg3)
	if cfg3.Web.WeatherRefreshMinutes != 5 {
		t.Fatalf("expected custom weather refresh preserved, got %d", cfg3.Web.WeatherRefreshMinutes)
	}
}
