package github

import (
	"path/filepath"
	"regexp"
	"testing"

	"botIAask/config"
)

type recordingBot struct {
	broadcasts []string
}

func (b *recordingBot) Broadcast(channels []string, message string) {
	b.broadcasts = append(b.broadcasts, message)
}
func (b *recordingBot) IsConnected() bool { return true }

func TestAnnounceNewEvents_DedupAndEventTypeFilter(t *testing.T) {
	d, err := NewDatabase(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	defer d.Close()
	c, err := NewCryptor(filepath.Join(t.TempDir(), "key"))
	if err != nil {
		t.Fatalf("NewCryptor: %v", err)
	}
	bot := &recordingBot{}
	f := NewFetcher(&config.Config{}, bot, d, c)

	// Avoid a real network call to the URL shortener in this test.
	oldShorten := shortenURLFunc
	shortenURLFunc = func(link, _ string) string { return link }
	t.Cleanup(func() { shortenURLFunc = oldShorten })

	repoCfg := config.GitHubTrackerRepoConfig{
		Owner: "owner", Repo: "repo", Channels: []string{"libera:#dev"},
		EventTypes: []string{"push"}, // filter out the release event below
	}

	events := []RawEvent{
		rawEvent("1", "PushEvent", "alice", "owner/repo", `{"ref":"refs/heads/main","size":1,"head":"abc","before":"","commits":[{"sha":"abc","message":"fix"}]}`),
		rawEvent("2", "ReleaseEvent", "", "owner/repo", `{"action":"published","release":{"tag_name":"v1","html_url":"x","author":{"login":"bob"}}}`),
	}

	f.announceNewEvents(repoCfg, "", 0, events)
	if len(bot.broadcasts) != 1 {
		t.Fatalf("expected only the push event announced (release filtered out), got %d broadcasts: %v", len(bot.broadcasts), bot.broadcasts)
	}

	// Re-running with the same events must not re-announce (dedup).
	f.announceNewEvents(repoCfg, "", 0, events)
	if len(bot.broadcasts) != 1 {
		t.Fatalf("expected no re-announcement on second pass, got %d broadcasts", len(bot.broadcasts))
	}
}

func TestAllowedEventTypeSet_EmptyFilterAllowsAll(t *testing.T) {
	allowed := allowedEventTypeSet(nil)
	for _, typ := range []string{"PushEvent", "PullRequestEvent", "ReleaseEvent", "WatchEvent"} {
		if !allowed(typ) {
			t.Fatalf("expected empty filter to allow %s", typ)
		}
	}
}

func TestAllowedEventTypeSet_RestrictsToConfigured(t *testing.T) {
	allowed := allowedEventTypeSet([]string{"push", "release"})
	if !allowed("PushEvent") || !allowed("ReleaseEvent") {
		t.Fatal("expected configured types to be allowed")
	}
	if allowed("PullRequestEvent") {
		t.Fatal("expected unconfigured type to be filtered out")
	}
}

func TestAllowedEventTypeSet_NewEventTypes(t *testing.T) {
	allowed := allowedEventTypeSet([]string{"issues", "create", "delete"})
	if !allowed("IssuesEvent") || !allowed("CreateEvent") || !allowed("DeleteEvent") {
		t.Fatal("expected issues/create/delete filter strings to map to their raw event types")
	}
	if allowed("PushEvent") {
		t.Fatal("expected push to be filtered out when not in the list")
	}
}

// TestAnnounceNewEvents_StoresPreCollapseMessage confirms MarkEventSeen persists each raw
// event's own RefID/Message, not the collapsed/suffixed broadcast text, so "!gh search"
// can find every real event even when a burst collapses to one broadcast line.
func TestAnnounceNewEvents_StoresPreCollapseMessage(t *testing.T) {
	d, err := NewDatabase(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	defer d.Close()
	c, err := NewCryptor(filepath.Join(t.TempDir(), "key"))
	if err != nil {
		t.Fatalf("NewCryptor: %v", err)
	}
	bot := &recordingBot{}
	f := NewFetcher(&config.Config{}, bot, d, c)
	oldShorten := shortenURLFunc
	shortenURLFunc = func(link, _ string) string { return link }
	t.Cleanup(func() { shortenURLFunc = oldShorten })

	repoCfg := config.GitHubTrackerRepoConfig{Owner: "owner", Repo: "repo", Channels: []string{"libera:#dev"}}
	events := []RawEvent{
		rawEvent("1", "PushEvent", "alice", "owner/repo", `{"ref":"refs/heads/main","head":"abc1234"}`),
		rawEvent("2", "PushEvent", "bob", "owner/repo", `{"ref":"refs/heads/main","head":"def5678"}`),
	}
	f.announceNewEvents(repoCfg, "", 0, events)

	// Broadcast collapses to one line, but both events must have their own stored row.
	if len(bot.broadcasts) != 1 {
		t.Fatalf("expected 1 collapsed broadcast, got %d: %v", len(bot.broadcasts), bot.broadcasts)
	}
	rows, err := d.SearchEvents("owner/repo", regexp.MustCompile("abc1234|def5678"), 10)
	if err != nil {
		t.Fatalf("SearchEvents: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 stored rows (one per raw event), got %d: %+v", len(rows), rows)
	}
}
