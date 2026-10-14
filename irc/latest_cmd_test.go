package irc

import (
	"bytes"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestLatestParsing(t *testing.T) {
	for in, want := range map[string]latestQuery{
		"nginx":         {"repology", "nginx"},
		"neovim/neovim": {"gh", "neovim/neovim"},
		"https://github.com/neovim/neovim/issues":   {"gh", "neovim/neovim"},
		"https://gitlab.com/gnome/gimp/-/tags":      {"gl", "gnome/gimp"},
		"gl:inkscape/inkscape":                      {"gl", "inkscape/inkscape"},
		"codeberg.org/forgejo/forgejo.git":          {"cb", "forgejo/forgejo"},
		"https://sourceforge.net/projects/keepass/": {"sf", "keepass"},
		"sf:keepass": {"sf", "keepass"},
	} {
		if got, err := parseLatestQuery(in); err != nil || got != want {
			t.Errorf("%q: got %v %v, want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"a/b/c", "../x", "gh:foo", "sf:a/b", "x y"} {
		if _, err := parseLatestQuery(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
	if got := highestTag([]string{"v1.9.0", "v1.10.0-rc1", "v1.10.0", "latest", "v1.2", "weekly.2012-03-27", "go1.25.3"}); got != "go1.25.3" {
		t.Errorf("highestTag = %q", got)
	}
	if got := sfVersion("/KeePass 2.x/2.57.1/KeePass-2.57.1.zip"); got != "2.57.1" {
		t.Errorf("sfVersion = %q", got)
	}
	if got := sfVersion("/files/7.4/setup.exe"); got != "7.4" {
		t.Errorf("sfVersion dir = %q", got)
	}
}

func TestWarnIfPrivateScope(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	scopeWarnOnce = sync.Once{}

	warnIfPrivateScope("public_repo, read:org")
	if buf.Len() != 0 {
		t.Fatalf("public_repo must not warn, got %q", buf.String())
	}
	warnIfPrivateScope("read:user, repo")
	warnIfPrivateScope("repo")
	if got := strings.Count(buf.String(), "[SECURITY]"); got != 1 {
		t.Fatalf("want exactly one warning, got %d: %q", got, buf.String())
	}
}
