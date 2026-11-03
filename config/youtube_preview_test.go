package config

import (
	"slices"
	"testing"
)

func TestNormalizeYouTubePreview(t *testing.T) {
	got, err := NormalizeYouTubePreview([]string{" #a ", "#A", "*", "&b"})
	if err != nil || !slices.Equal(got, []string{"#a", "*", "&b"}) {
		t.Fatalf("got %v %v", got, err)
	}
	for _, bad := range []string{"a", "#", "#a b", "#a,#b", "#a\r\nQUIT"} {
		if _, err := NormalizeYouTubePreview([]string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	n := IRCNetworkConfig{YouTubePreview: []string{"#a"}}
	if !n.YouTubePreviewOn("#A") || n.YouTubePreviewOn("#b") {
		t.Fatal("YouTubePreviewOn")
	}
}
