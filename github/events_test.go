package github

import (
	"strings"
	"testing"
)

func rawEvent(id, typ, actor, repo, payload string) RawEvent {
	ev := RawEvent{ID: id, Type: typ, Payload: []byte(payload)}
	ev.Actor.Login = actor
	ev.Repo.Name = repo
	return ev
}

func TestExtractAnnouncement_Push(t *testing.T) {
	ev := rawEvent("1", "PushEvent", "alice", "owner/repo", `{
		"ref": "refs/heads/main",
		"size": 2,
		"head": "abcd1234",
		"before": "9876fedc",
		"commits": [
			{"sha": "9876fedc", "message": "earlier commit"},
			{"sha": "abcd1234", "message": "fix: handle nil ptr\n\nlonger body"}
		]
	}`)
	ann, ok := ExtractAnnouncement(ev, RepoMeta{})
	if !ok {
		t.Fatal("expected push to be announced")
	}
	if ann.Kind != "push" {
		t.Fatalf("expected kind push, got %q", ann.Kind)
	}
	if !strings.Contains(ann.Message, "alice") || !strings.Contains(ann.Message, "main") ||
		!strings.Contains(ann.Message, "fix: handle nil ptr") || !strings.Contains(ann.Message, "+1 more") {
		t.Fatalf("unexpected push message: %q", ann.Message)
	}
	if !strings.Contains(ann.Message, "compare/9876fedc...abcd1234") {
		t.Fatalf("expected compare link for multi-commit push, got %q", ann.Message)
	}
}

func TestExtractAnnouncement_PullRequest(t *testing.T) {
	ev := rawEvent("2", "PullRequestEvent", "", "owner/repo", `{
		"action": "opened",
		"number": 42,
		"pull_request": {
			"html_url": "https://github.com/owner/repo/pull/42",
			"title": "Fix bug in X",
			"user": {"login": "bob"},
			"merged": false
		}
	}`)
	ann, ok := ExtractAnnouncement(ev, RepoMeta{})
	if !ok {
		t.Fatal("expected PR open to be announced")
	}
	if !strings.Contains(ann.Message, "bob") || !strings.Contains(ann.Message, "opened PR #42") ||
		!strings.Contains(ann.Message, "Fix bug in X") {
		t.Fatalf("unexpected PR message: %q", ann.Message)
	}
}

func TestExtractAnnouncement_PullRequestMergedNotClosed(t *testing.T) {
	ev := rawEvent("3", "PullRequestEvent", "", "owner/repo", `{
		"action": "closed",
		"number": 7,
		"pull_request": {"html_url": "x", "title": "t", "user": {"login": "bob"}, "merged": true}
	}`)
	ann, ok := ExtractAnnouncement(ev, RepoMeta{})
	if !ok || !strings.Contains(ann.Message, "merged PR #7") {
		t.Fatalf("expected merged PR message, got ok=%v msg=%q", ok, ann.Message)
	}
}

func TestExtractAnnouncement_PullRequestSynchronizeSkipped(t *testing.T) {
	ev := rawEvent("4", "PullRequestEvent", "", "owner/repo", `{"action": "synchronize", "number": 1, "pull_request": {}}`)
	if _, ok := ExtractAnnouncement(ev, RepoMeta{}); ok {
		t.Fatal("expected synchronize action to be skipped")
	}
}

func TestExtractAnnouncement_Release(t *testing.T) {
	ev := rawEvent("5", "ReleaseEvent", "", "owner/repo", `{
		"action": "published",
		"release": {
			"tag_name": "v1.2.3",
			"name": "Release 1.2.3",
			"html_url": "https://github.com/owner/repo/releases/tag/v1.2.3",
			"author": {"login": "carol"}
		}
	}`)
	ann, ok := ExtractAnnouncement(ev, RepoMeta{})
	if !ok {
		t.Fatal("expected published release to be announced")
	}
	if !strings.Contains(ann.Message, "carol") || !strings.Contains(ann.Message, "v1.2.3") ||
		!strings.Contains(ann.Message, "Release 1.2.3") {
		t.Fatalf("unexpected release message: %q", ann.Message)
	}
}

func TestExtractAnnouncement_ReleaseDraftSkipped(t *testing.T) {
	ev := rawEvent("6", "ReleaseEvent", "", "owner/repo", `{"action": "created", "release": {"tag_name": "v0.0.1"}}`)
	if _, ok := ExtractAnnouncement(ev, RepoMeta{}); ok {
		t.Fatal("expected non-published release action to be skipped")
	}
}

func TestExtractAnnouncement_UnknownTypeSkipped(t *testing.T) {
	ev := rawEvent("7", "WatchEvent", "someone", "owner/repo", `{}`)
	if _, ok := ExtractAnnouncement(ev, RepoMeta{}); ok {
		t.Fatal("expected unknown event type to be skipped")
	}
}

func TestEventKey_StableAcrossReruns(t *testing.T) {
	k1 := EventKey("owner", "repo", "123")
	k2 := EventKey("owner", "repo", "123")
	if k1 != k2 {
		t.Fatalf("expected stable key, got %q vs %q", k1, k2)
	}
	if k1 != "owner/repo#123" {
		t.Fatalf("unexpected key shape: %q", k1)
	}
}
