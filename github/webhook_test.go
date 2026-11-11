package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"botIAask/config"
)

type chanBot struct{ out chan string }

func (b chanBot) Broadcast(_ []string, msg string) { b.out <- msg }
func (chanBot) IsConnected() bool                  { return true }

func sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func TestHandleWebhook(t *testing.T) {
	d, err := NewDatabase(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	c, err := NewCryptor(filepath.Join(t.TempDir(), "key"))
	if err != nil {
		t.Fatal(err)
	}
	old := shortenURLFunc
	shortenURLFunc = func(link, _ string) string { return link }
	t.Cleanup(func() { shortenURLFunc = old })

	bot := chanBot{make(chan string, 4)}
	cfg := &config.Config{GitHubTracker: config.GitHubTrackerConfig{
		Enabled: true, WebhookSecret: "s3cret",
		Repos: []config.GitHubTrackerRepoConfig{
			{Owner: "Owner", Repo: "hooked", Channels: []string{"libera:#dev"}, Webhook: true},
			{Owner: "owner", Repo: "polled"},
		},
	}}
	f := NewFetcher(cfg, bot, d, c)

	push := func(repo, ref string) []byte {
		return []byte(`{"ref":"` + ref + `","before":"1111111","after":"2222222abcdef","commits":[{"id":"2222222abcdef","message":"fix the thing\n\nbody"}],` +
			`"repository":{"full_name":"owner/` + repo + `"},"sender":{"login":"alice"}}`)
	}
	deliver := func(event, id, secret string, body []byte) int {
		return f.HandleWebhook(event, id, sign(secret, body), body)
	}

	if got := deliver("push", "d0", "wrong", push("hooked", "refs/heads/main")); got != http.StatusUnauthorized {
		t.Fatalf("bad signature: got %d, want 401", got)
	}
	if got := deliver("ping", "d0", "s3cret", []byte(`{}`)); got != http.StatusOK {
		t.Fatalf("ping: got %d, want 200", got)
	}
	if got := deliver("push", "d1", "s3cret", push("polled", "refs/heads/main")); got != http.StatusNotFound {
		t.Fatalf("repo not in webhook mode: got %d, want 404", got)
	}
	if got := deliver("push", "d2", "s3cret", push("hooked", "refs/tags/v1")); got != http.StatusNoContent {
		t.Fatalf("tag push: got %d, want 204 (its create event announces it)", got)
	}
	if got := deliver("watch", "d3", "s3cret", []byte(`{}`)); got != http.StatusNoContent {
		t.Fatalf("unannounced event type: got %d, want 204", got)
	}
	if got := deliver("push", "d4", "s3cret", push("hooked", "refs/heads/main")); got != http.StatusAccepted {
		t.Fatalf("push: got %d, want 202", got)
	}
	select {
	case msg := <-bot.out:
		if !strings.Contains(msg, "fix the thing") || !strings.Contains(msg, "2222222") {
			t.Fatalf("unexpected announcement: %q", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("push was not announced")
	}

	// Same delivery id again (GitHub redelivery) must not announce twice.
	if got := deliver("push", "d4", "s3cret", push("hooked", "refs/heads/main")); got != http.StatusAccepted {
		t.Fatalf("redelivery: got %d, want 202", got)
	}
	select {
	case msg := <-bot.out:
		t.Fatalf("redelivery announced again: %q", msg)
	case <-time.After(300 * time.Millisecond):
	}
}
