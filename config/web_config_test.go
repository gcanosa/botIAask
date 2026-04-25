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
	cfg2 := &Config{Web: WebConfig{ServerLocation: "Tokyo"}}
	applyWebDefaults(cfg2)
	if cfg2.Web.ServerLocation != "Tokyo" {
		t.Fatalf("expected custom location preserved, got %q", cfg2.Web.ServerLocation)
	}
}
