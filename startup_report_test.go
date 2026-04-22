package main

import (
	"bytes"
	"strings"
	"testing"

	"botIAask/config"
	"botIAask/meta"
)

func TestPrintDaemonParentReport_plainNoColor(t *testing.T) {
	cfg := &config.Config{
		IRC: config.IRCConfig{
			Server:   "irc.example.net",
			Port:     6697,
			UseSSL:   true,
			Nickname: "testbot",
		},
		AI: config.AIConfig{
			LMStudioURL: "http://127.0.0.1:1234",
			Model:       "test-model",
		},
		Daemon: config.DaemonConfig{
			PIDFile: "data/daemon.pid",
		},
		Web: config.WebConfig{
			Enabled: false,
			Host:    "127.0.0.1",
			Port:    8080,
		},
		RSS: config.RSSConfig{
			Enabled:         false,
			IntervalMinutes: 15,
		},
		Stats: config.StatsConfig{
			Enabled:  false,
			Interval: 60,
		},
		Logger: config.LoggerConfig{
			RotationDays: 0,
		},
	}

	var buf bytes.Buffer
	printAppIdentity(&buf, false)
	printDaemonParentReport(&buf, cfg, "config/config.yaml", false)
	out := buf.String()

	for _, sub := range []string{
		meta.Name,
		meta.Version,
		meta.Author,
		"── Process ──",
		"Config file",
		"config/config.yaml",
		"PID file",
		"irc.example.net:6697",
		"NOT STARTED",
		"RSS fetcher",
		"OFF",
	} {
		if !strings.Contains(out, sub) {
			t.Fatalf("output missing %q:\n%s", sub, out)
		}
	}
}
