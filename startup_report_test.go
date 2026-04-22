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

func TestPrintDaemonSpawnResult_plainNoColor(t *testing.T) {
	cfg := &config.Config{
		Daemon: config.DaemonConfig{PIDFile: "data/daemon.pid"},
	}
	var buf bytes.Buffer
	printDaemonSpawnResult(&buf, cfg, 4242, false)
	out := buf.String()
	for _, sub := range []string{
		"── Start ──",
		"Child process",
		"4242",
		"spawned",
		"stdio",
		"detached",
		"data/daemon.pid",
		"written by child on boot",
	} {
		if !strings.Contains(out, sub) {
			t.Fatalf("output missing %q:\n%s", sub, out)
		}
	}
}

func TestPrintStopResult_plainNoColor(t *testing.T) {
	cfg := &config.Config{
		Daemon: config.DaemonConfig{PIDFile: "data/daemon.pid"},
	}
	for _, tc := range []struct {
		kind     StopResultKind
		substrs  []string
		notWants []string
	}{
		{
			kind: StopExitedNormal,
			substrs: []string{
				"── Stop ──",
				"SIGTERM",
				"12345",
				"exited",
				"removed",
				"stopped successfully",
			},
			notWants: []string{"SIGKILL"},
		},
		{
			kind: StopForceKilled,
			substrs: []string{
				"── Stop ──",
				"SIGKILL",
				"12345",
				"killed",
				"removed",
				"force-stopped",
			},
			notWants: []string{"SIGTERM"},
		},
	} {
		var buf bytes.Buffer
		printStopResult(&buf, cfg, 12345, tc.kind, false)
		out := buf.String()
		for _, sub := range tc.substrs {
			if !strings.Contains(out, sub) {
				t.Fatalf("kind %v: output missing %q:\n%s", tc.kind, sub, out)
			}
		}
		for _, s := range tc.notWants {
			if strings.Contains(out, s) {
				t.Fatalf("kind %v: output should not contain %q:\n%s", tc.kind, s, out)
			}
		}
	}
}
