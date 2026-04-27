package config

import (
	"strings"
	"testing"
)

func TestRehashDiff_IRCChannels(t *testing.T) {
	f := false
	before := &Config{
		IRC: IRCConfig{Channels: []IRChannel{
			{Name: "#a"},
			{Name: "#b", AutoJoin: &f},
		}},
		Stats:   StatsConfig{Enabled: true, Interval: 60},
		RSS:     RSSConfig{Enabled: true, IntervalMinutes: 30},
		Web:     WebConfig{Enabled: true, Port: 3366, Host: "0.0.0.0"},
		Logger:  LoggerConfig{RotationDays: 7},
		Uploads: UploadsConfig{MaxFileMB: 200},
	}
	after := &Config{
		IRC: IRCConfig{Channels: []IRChannel{
			{Name: "#a"},
			{Name: "#c"},
		}},
		Stats:   StatsConfig{Enabled: true, Interval: 60},
		RSS:     RSSConfig{Enabled: true, IntervalMinutes: 30},
		Web:     WebConfig{Enabled: true, Port: 3366, Host: "0.0.0.0"},
		Logger:  LoggerConfig{RotationDays: 7},
		Uploads: UploadsConfig{MaxFileMB: 200},
	}
	lines := RehashDiff(before, after)
	s := strings.Join(lines, " ")
	if !strings.Contains(s, "removed") && !strings.Contains(s, "added") {
		t.Fatalf("expected autoin add/remove, got: %q", s)
	}
}

func TestRehashDiff_RSSToggle(t *testing.T) {
	before := &Config{
		RSS:     RSSConfig{Enabled: true, IntervalMinutes: 10},
		Stats:   StatsConfig{Interval: 60},
		Uploads: UploadsConfig{MaxFileMB: 200},
	}
	after := &Config{
		RSS:     RSSConfig{Enabled: false, IntervalMinutes: 10},
		Stats:   StatsConfig{Interval: 60},
		Uploads: UploadsConfig{MaxFileMB: 200},
	}
	lines := RehashDiff(before, after)
	if !hasLineContaining(lines, "RSS: enabled true -> false") {
		t.Fatalf("expected RSS toggle line, got %q", lines)
	}
}

func TestRehashDiff_Web(t *testing.T) {
	before := &Config{
		Web:     WebConfig{Enabled: true, Port: 80, Host: "127.0.0.1"},
		Stats:   StatsConfig{Interval: 60},
		RSS:     RSSConfig{},
		Uploads: UploadsConfig{MaxFileMB: 200},
	}
	after := &Config{
		Web:     WebConfig{Enabled: false, Port: 80, Host: "127.0.0.1"},
		Stats:   StatsConfig{Interval: 60},
		RSS:     RSSConfig{},
		Uploads: UploadsConfig{MaxFileMB: 200},
	}
	lines := RehashDiff(before, after)
	if !hasLineContaining(lines, "Web: enabled true -> false") {
		t.Fatalf("expected web enabled line, got %q", lines)
	}
}

func TestRehashDiff_IRCEndpoint(t *testing.T) {
	before := &Config{
		IRC:     IRCConfig{Server: "a.net", Port: 6697, Nickname: "B1", UseSSL: true},
		Stats:   StatsConfig{Interval: 60},
		Uploads: UploadsConfig{MaxFileMB: 200},
	}
	after := &Config{
		IRC:     IRCConfig{Server: "b.net", Port: 6697, Nickname: "B1", UseSSL: true},
		Stats:   StatsConfig{Interval: 60},
		Uploads: UploadsConfig{MaxFileMB: 200},
	}
	lines := RehashDiff(before, after)
	if !hasLineContaining(lines, "reconnect required") {
		t.Fatalf("expected reconnect note, got %q", lines)
	}
}

func TestCloneConfig(t *testing.T) {
	orig := &Config{IRC: IRCConfig{Channels: []IRChannel{{Name: "#x"}}}, Stats: StatsConfig{Interval: 60}, Uploads: UploadsConfig{MaxFileMB: 200}}
	c, err := CloneConfig(orig)
	if err != nil {
		t.Fatal(err)
	}
	c.IRC.Channels[0].Name = "#y"
	if orig.IRC.Channels[0].Name != "#x" {
		t.Fatalf("mutating clone should not affect orig")
	}
}

func hasLineContaining(lines []string, sub string) bool {
	for _, ln := range lines {
		if strings.Contains(ln, sub) {
			return true
		}
	}
	return false
}
