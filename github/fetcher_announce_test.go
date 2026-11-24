package github

import (
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"botIAask/config"
)

type recordingBot struct {
	broadcasts []string
}

func (b *recordingBot) Broadcast(channels []string, message string) {
	b.broadcasts = append(b.broadcasts, message)
}
func (b *recordingBot) IsConnected() bool { return true }

func newAnnounceFetcher(t *testing.T, bot BotInterface) (*Fetcher, *Database) {
	t.Helper()
	d, err := NewDatabase(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	c, err := NewCryptor(filepath.Join(t.TempDir(), "key"))
	if err != nil {
		t.Fatalf("NewCryptor: %v", err)
	}
	// No real shortener calls, no pacing sleeps.
	oldShorten, oldPace := shortenURLFunc, announcePace
	shortenURLFunc, announcePace = func(link, _ string) string { return link }, 0
	t.Cleanup(func() { shortenURLFunc, announcePace = oldShorten, oldPace })
	return NewFetcher(&config.Config{}, bot, d, c), d
}

func TestAnnounce_DedupAndEventTypeFilter(t *testing.T) {
	bot := &recordingBot{}
	f, _ := newAnnounceFetcher(t, bot)
	repoCfg := config.GitHubTrackerRepoConfig{
		Owner: "owner", Repo: "repo", Channels: []string{"libera:#dev"},
		EventTypes: []string{"release"}, // filter out the issue event below
	}
	events := []RawEvent{
		rawEvent("1", "ReleaseEvent", "", "owner/repo", `{"action":"published","release":{"tag_name":"v1","html_url":"x","author":{"login":"bob"}}}`),
		rawEvent("2", "IssuesEvent", "", "owner/repo", `{"action":"opened","issue":{"number":1,"title":"t"}}`),
	}

	f.announce(repoCfg, eventAnnouncements(repoCfg, events), false)
	if len(bot.broadcasts) != 1 {
		t.Fatalf("expected only the release announced (issue filtered out), got %d: %v", len(bot.broadcasts), bot.broadcasts)
	}
	f.announce(repoCfg, eventAnnouncements(repoCfg, events), false)
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

// A burst prints oldest first, only the newest maxLines, with one summary line in front; every
// item is still recorded so "!gh search" and the dedup see all of them.
func TestAnnounce_BurstIsCappedChronologicalAndRecorded(t *testing.T) {
	bot := &recordingBot{}
	f, d := newAnnounceFetcher(t, bot)
	repoCfg := config.GitHubTrackerRepoConfig{Owner: "owner", Repo: "repo", Channels: []string{"libera:#dev"}}

	var anns []Announcement
	base := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	for i := 7; i >= 0; i-- { // deliberately handed over newest first: announce must sort
		anns = append(anns, commitAnnouncement("owner/repo", "alice", "main", fmt.Sprintf("sha%04d", i), fmt.Sprintf("commit %d", i), base.Add(time.Duration(i)*time.Minute)))
	}
	f.announce(repoCfg, anns, false)

	if len(bot.broadcasts) != maxLines+1 {
		t.Fatalf("want summary + %d lines, got %d: %v", maxLines, len(bot.broadcasts), bot.broadcasts)
	}
	if !strings.Contains(bot.broadcasts[0], "+3 earlier") {
		t.Errorf("summary line: %q", bot.broadcasts[0])
	}
	for i := 0; i < maxLines; i++ {
		if want := fmt.Sprintf("commit %d", 3+i); !strings.Contains(bot.broadcasts[1+i], want) {
			t.Errorf("line %d should be %q, got %q", i, want, bot.broadcasts[1+i])
		}
	}
	rows, _ := d.SearchEvents("owner/repo", regexp.MustCompile("commit"), 20)
	if len(rows) != 8 {
		t.Fatalf("want all 8 recorded, got %d", len(rows))
	}
}

// First poll of a repo records history without printing it.
func TestAnnounce_SilentRecordsWithoutPrinting(t *testing.T) {
	bot := &recordingBot{}
	f, _ := newAnnounceFetcher(t, bot)
	repoCfg := config.GitHubTrackerRepoConfig{Owner: "owner", Repo: "repo", Channels: []string{"libera:#dev"}}
	a := commitAnnouncement("owner/repo", "alice", "main", "abc1234", "old", time.Now())
	f.announce(repoCfg, []Announcement{a}, true)
	f.announce(repoCfg, []Announcement{a}, false)
	if len(bot.broadcasts) != 0 {
		t.Fatalf("silent pass must record, so the later pass has nothing new: %v", bot.broadcasts)
	}
}

// The same commit reaching two branches (or the poller and a webhook) prints once.
func TestAnnounce_CommitDedupedBySHA(t *testing.T) {
	bot := &recordingBot{}
	f, _ := newAnnounceFetcher(t, bot)
	repoCfg := config.GitHubTrackerRepoConfig{Owner: "owner", Repo: "repo", Channels: []string{"libera:#dev"}}
	at := time.Now()
	f.announce(repoCfg, []Announcement{
		commitAnnouncement("owner/repo", "a", "feature", "abc1234", "x", at),
		commitAnnouncement("owner/repo", "a", "main", "abc1234", "x", at),
	}, false)
	if len(bot.broadcasts) != 1 {
		t.Fatalf("want 1, got %v", bot.broadcasts)
	}
}

// Full cycle against a fake API: first poll only records heads; a later push prints one line
// per commit, oldest first, and nothing is repeated on the next 304/identical poll.
func TestPollRepo_BranchHeadsOneLinePerCommit(t *testing.T) {
	bot := &recordingBot{}
	f, _ := newAnnounceFetcher(t, bot)
	head := "aaa1111"
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/branches"):
			fmt.Fprintf(w, `[{"name":"main","commit":{"sha":%q}}]`, head)
		case strings.HasSuffix(r.URL.Path, "/commits"):
			fmt.Fprint(w, `[
				{"sha":"ccc3333","commit":{"message":"third","author":{"name":"A"},"committer":{"date":"2026-11-24T10:02:00Z"}},"author":{"login":"alice"}},
				{"sha":"bbb2222","commit":{"message":"second","author":{"name":"A"},"committer":{"date":"2026-11-24T10:01:00Z"}},"author":{"login":"alice"}},
				{"sha":"aaa1111","commit":{"message":"first","author":{"name":"A"},"committer":{"date":"2026-11-24T10:00:00Z"}},"author":{"login":"alice"}}]`)
		default: // events
			w.Header().Set("ETag", `"e1"`)
			fmt.Fprint(w, `[]`)
		}
	})
	repoCfg := config.GitHubTrackerRepoConfig{Owner: "owner", Repo: "repo", Channels: []string{"libera:#dev"}}

	f.pollRepo(repoCfg) // first poll: baseline
	if len(bot.broadcasts) != 0 {
		t.Fatalf("baseline must stay silent: %v", bot.broadcasts)
	}
	head = "ccc3333"
	f.pollRepo(repoCfg)
	if len(bot.broadcasts) != 2 || !strings.Contains(bot.broadcasts[0], ": second") || !strings.Contains(bot.broadcasts[1], ": third") {
		t.Fatalf("want second then third, got %v", bot.broadcasts)
	}
	f.pollRepo(repoCfg)
	if len(bot.broadcasts) != 2 {
		t.Fatalf("nothing new, nothing printed: %v", bot.broadcasts)
	}
}
