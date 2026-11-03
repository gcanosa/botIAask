package irc

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"botIAask/config"
)

func TestYoutubeLink(t *testing.T) {
	for _, c := range []struct {
		msg, id string
		start   time.Duration
	}{
		{"https://youtu.be/gxl_p3ZOUno?t=1340", "gxl_p3ZOUno", 1340 * time.Second},
		{"look https://www.youtube.com/watch?list=x&v=gxl_p3ZOUno&t=22m20s.", "gxl_p3ZOUno", 1340 * time.Second},
		{"https://youtube.com/shorts/abcdefghijk", "abcdefghijk", 0},
		{"https://m.youtube.com/live/abcdefghijk?si=x", "abcdefghijk", 0},
		{"https://example.com/watch?v=abcdefghijk", "", 0},
		{"https://youtube.com/watch?v=short", "", 0},
	} {
		id, start, _ := youtubeLink(c.msg)
		if id != c.id || start != c.start {
			t.Errorf("%q: got %q %v, want %q %v", c.msg, id, start, c.id, c.start)
		}
	}
}

func TestFormatYouTube(t *testing.T) {
	v := &ytVideo{Title: "T", Channel: "C", Length: 3526 * time.Second, Views: 145501, Likes: 259, HaveLikes: true,
		Published: time.Now().Add(-74 * 24 * time.Hour)}
	want := "\x0301,00You\x0300,04Tube\x0F \x02T\x02 | @22:20 | 58:46 | Channel: \x02C\x02 | 145.5K views | 👍 259 | 74d ago"
	if got := formatYouTube(v, 1340*time.Second); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// !youtube on/off edits this network's youtube_preview list and the read path honours it.
func TestYouTubeCommand(t *testing.T) {
	n, out := accessNetwork(t, nil)
	path := filepath.Join(t.TempDir(), "config.yaml")
	n.configPath = path
	n.rehashHook = func(string) error {
		c, err := config.LoadConfig(path)
		if err == nil {
			n.cfg.Store(c)
		}
		return err
	}
	n.handleYouTubeCommand("#c", []string{"on"})
	n.handleYouTubeCommand("alice", strings.Fields("on #D"))
	n.handleYouTubeCommand("#c", strings.Fields("off #d"))
	n.handleYouTubeCommand("#c", nil)
	n.handleYouTubeCommand("alice", []string{"on"}) // PM with no channel
	if !n.netCfg().YouTubePreviewOn("#C") || n.netCfg().YouTubePreviewOn("#d") {
		t.Fatalf("list = %v", n.netCfg().YouTubePreview)
	}
	got := strings.Join(out.all(), "\n")
	want := "PRIVMSG #c YouTube previews on for #c.\nPRIVMSG alice YouTube previews on for #D.\nPRIVMSG #c YouTube previews off for #d.\n" +
		"PRIVMSG #c YouTube previews on test: #c\nPRIVMSG alice Usage: !youtube [on|off] [#chan|*]"
	if got != want {
		t.Fatalf("got:\n%s", got)
	}
}
