package github

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func rawEvent(id, typ, actor, repo, payload string) RawEvent {
	ev := RawEvent{ID: id, Type: typ, Payload: []byte(payload)}
	ev.Actor.Login = actor
	ev.Repo.Name = repo
	return ev
}

// Pushes never come out of ExtractAnnouncement: the Events API lags them by up to hours,
// so branch polling (and webhooks) own commits.
func TestExtractAnnouncement_PushSkipped(t *testing.T) {
	ev := rawEvent("1", "PushEvent", "alice", "owner/repo", `{"ref":"refs/heads/main","head":"abcd1234","before":"9876fedc"}`)
	if _, ok := ExtractAnnouncement(ev, RepoMeta{}); ok {
		t.Fatal("expected PushEvent to be skipped")
	}
}

func TestWebhookPush_OneLinePerCommitOldestFirst(t *testing.T) {
	ev := rawEvent("d1", "PushEvent", "alice", "owner/repo", `{
		"ref": "refs/heads/main",
		"commits": [
			{"id": "1111111aaaa", "message": "first\n\nbody", "timestamp": "2026-11-24T10:00:00Z", "author": {"name": "Alice A", "username": "alice"}},
			{"id": "2222222bbbb", "message": "second", "timestamp": "2026-11-24T10:01:00Z", "author": {"name": "Bob B"}}
		]
	}`)
	anns := webhookPush(ev)
	if len(anns) != 2 {
		t.Fatalf("want 2 lines, got %d: %v", len(anns), anns)
	}
	if !strings.Contains(anns[0].Message, "alice") || !strings.Contains(anns[0].Message, ": first") || strings.Contains(anns[0].Message, "body") {
		t.Errorf("first line: %q", anns[0].Message)
	}
	if !strings.Contains(anns[1].Message, "Bob B") || !strings.Contains(anns[1].Message, "/commit/2222222bbbb") {
		t.Errorf("second line: %q", anns[1].Message)
	}
	if anns[0].ID != "1111111aaaa" || anns[0].Kind != "push" || anns[0].RefID != "1111111" {
		t.Errorf("unexpected id/kind/ref: %+v", anns[0])
	}
}

func TestWebhookPush_IgnoresTagsAndEventsAPIPayloads(t *testing.T) {
	tag := rawEvent("d2", "PushEvent", "alice", "owner/repo", `{"ref":"refs/tags/v1","commits":[{"id":"abc","message":"x"}]}`)
	api := rawEvent("d3", "PushEvent", "alice", "owner/repo", `{"ref":"refs/heads/main","head":"abc","commits":[{"sha":"abc","message":"x"}]}`)
	if len(webhookPush(tag)) != 0 || len(webhookPush(api)) != 0 {
		t.Fatal("tag pushes and Events API commit lists must not announce")
	}
}

func commitOf(sha, msg, login string, at string) Commit {
	var c Commit
	c.SHA, c.Commit.Message = sha, msg
	c.Commit.Committer.Date, _ = time.Parse(time.RFC3339, at)
	if login != "" {
		c.Author = &struct {
			Login string `json:"login"`
		}{login}
	} else {
		c.Commit.Author.Name = "Plain Name"
	}
	return c
}

func TestBranchAnnouncements_StopsAtOldHeadOldestFirst(t *testing.T) {
	commits := []Commit{ // newest first, as the API returns them
		commitOf("ccc3333", "third", "alice", "2026-11-24T10:02:00Z"),
		commitOf("bbb2222", "second", "", "2026-11-24T10:01:00Z"),
		commitOf("aaa1111", "already announced", "alice", "2026-11-24T10:00:00Z"),
		commitOf("000", "ancient", "alice", "2026-11-24T09:00:00Z"),
	}
	anns := branchAnnouncements("owner/repo", "main", "aaa1111", commits)
	if len(anns) != 2 || anns[0].ID != "bbb2222" || anns[1].ID != "ccc3333" {
		t.Fatalf("want [bbb2222 ccc3333], got %+v", anns)
	}
	if !strings.Contains(anns[0].Message, "Plain Name") || !strings.Contains(anns[1].Message, "alice") {
		t.Errorf("author fallback wrong: %q / %q", anns[0].Message, anns[1].Message)
	}
}

func TestBranchAnnouncements_UnknownOldHeadTakesEverything(t *testing.T) { // force push
	commits := []Commit{commitOf("bbb", "b", "a", "2026-11-24T10:01:00Z"), commitOf("aaa", "a", "a", "2026-11-24T10:00:00Z")}
	if n := len(branchAnnouncements("o/r", "main", "gone", commits)); n != 2 {
		t.Fatalf("want 2, got %d", n)
	}
}

// An amended/rebased chain can have a child dated before its parent; the lines must keep
// chain order after the chronological sort in announce().
func TestBranchAnnouncements_ChainOrderSurvivesBackwardsDates(t *testing.T) {
	commits := []Commit{commitOf("child", "c", "a", "2026-11-24T09:00:00Z"), commitOf("parent", "p", "a", "2026-11-24T10:00:00Z")}
	anns := branchAnnouncements("o/r", "main", "none", commits)
	if anns[0].ID != "parent" || anns[1].At.Before(anns[0].At) {
		t.Fatalf("chain order broken: %+v", anns)
	}
}

func TestCommitAnnouncement_ClipsLongTitle(t *testing.T) {
	long := strings.Repeat("é", 400) // multibyte: must clip on runes, not bytes
	ann := commitAnnouncement("o/r", "a", "main", "abcdef1234", long, time.Time{})
	if n := strings.Count(ann.Message, "é"); n != maxTitle-1 || !strings.Contains(ann.Message, "…") {
		t.Fatalf("want %d runes + ellipsis, got %d: %q", maxTitle-1, n, ann.Message)
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
	if !strings.Contains(ann.Message, "bob") || !strings.Contains(ann.Message, "[PR #42]") ||
		!strings.Contains(ann.Message, "opened") || !strings.Contains(ann.Message, "Fix bug in X") {
		t.Fatalf("unexpected PR message: %q", ann.Message)
	}
	if ann.RefID != "#42" {
		t.Fatalf("expected RefID #42, got %q", ann.RefID)
	}
}

func TestExtractAnnouncement_PullRequestMergedNotClosed(t *testing.T) {
	ev := rawEvent("3", "PullRequestEvent", "", "owner/repo", `{
		"action": "closed",
		"number": 7,
		"pull_request": {"html_url": "x", "title": "t", "user": {"login": "bob"}, "merged": true}
	}`)
	ann, ok := ExtractAnnouncement(ev, RepoMeta{})
	if !ok || !strings.Contains(ann.Message, "[PR #7]") || !strings.Contains(ann.Message, "merged") {
		t.Fatalf("expected merged PR message, got ok=%v msg=%q", ok, ann.Message)
	}
}

// TestExtractAnnouncement_PullRequestTrimmedPayload covers the shape GitHub's public
// Events API actually returns today: no title/html_url/user, and a merge reported as
// action "merged" directly rather than "closed" + merged:true.
func TestExtractAnnouncement_PullRequestTrimmedPayload(t *testing.T) {
	ev := rawEvent("2b", "PullRequestEvent", "carol", "owner/repo", `{
		"action": "opened",
		"number": 42,
		"pull_request": {
			"head": {"ref": "fix/thing"},
			"base": {"ref": "main"}
		}
	}`)
	ann, ok := ExtractAnnouncement(ev, RepoMeta{})
	if !ok {
		t.Fatal("expected trimmed PR open to be announced")
	}
	if !strings.Contains(ann.Message, "carol") || !strings.Contains(ann.Message, "[PR #42]") ||
		!strings.Contains(ann.Message, "opened") || !strings.Contains(ann.Message, "fix/thing -> main") ||
		!strings.Contains(ann.Message, "github.com/owner/repo/pull/42") {
		t.Fatalf("unexpected trimmed PR message: %q", ann.Message)
	}
}

func TestExtractAnnouncement_PullRequestMergedActionTrimmedPayload(t *testing.T) {
	ev := rawEvent("3b", "PullRequestEvent", "carol", "owner/repo", `{
		"action": "merged",
		"number": 7,
		"pull_request": {"head": {"ref": "fix/thing"}, "base": {"ref": "main"}}
	}`)
	ann, ok := ExtractAnnouncement(ev, RepoMeta{})
	if !ok || !strings.Contains(ann.Message, "[PR #7]") || !strings.Contains(ann.Message, "merged") {
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

func TestExtractAnnouncement_Issue_OpenedAndClosed(t *testing.T) {
	for _, action := range []string{"opened", "closed"} {
		ev := rawEvent("8", "IssuesEvent", "dave", "owner/repo", fmt.Sprintf(`{
			"action": "%s",
			"issue": {"number": 45, "title": "Something broke", "html_url": "https://github.com/owner/repo/issues/45", "user": {"login": "dave"}}
		}`, action))
		ann, ok := ExtractAnnouncement(ev, RepoMeta{})
		if !ok {
			t.Fatalf("expected issue %q to be announced", action)
		}
		if ann.RefID != "#45" || !strings.Contains(ann.Message, "[ISSUE #45]") ||
			!strings.Contains(ann.Message, action) || !strings.Contains(ann.Message, "Something broke") {
			t.Fatalf("unexpected issue message for action %q: %q", action, ann.Message)
		}
	}
}

func TestExtractAnnouncement_Issue_ActionNotAnnounced(t *testing.T) {
	ev := rawEvent("8b", "IssuesEvent", "dave", "owner/repo", `{"action": "labeled", "issue": {"number": 45}}`)
	if _, ok := ExtractAnnouncement(ev, RepoMeta{}); ok {
		t.Fatal("expected non-opened/closed issue action to be skipped")
	}
}

func TestExtractAnnouncement_Create_Branch(t *testing.T) {
	ev := rawEvent("9", "CreateEvent", "erin", "owner/repo", `{"ref_type": "branch", "ref": "feature/x"}`)
	ann, ok := ExtractAnnouncement(ev, RepoMeta{})
	if !ok {
		t.Fatal("expected branch creation to be announced")
	}
	if ann.RefID != "feature/x" || !strings.Contains(ann.Message, "[BRANCH feature/x]") ||
		!strings.Contains(ann.Message, "erin") || !strings.Contains(ann.Message, "created") {
		t.Fatalf("unexpected create message: %q", ann.Message)
	}
}

func TestExtractAnnouncement_Create_Tag(t *testing.T) {
	ev := rawEvent("9b", "CreateEvent", "erin", "owner/repo", `{"ref_type": "tag", "ref": "v2.0"}`)
	ann, ok := ExtractAnnouncement(ev, RepoMeta{})
	if !ok || !strings.Contains(ann.Message, "[TAG v2.0]") {
		t.Fatalf("unexpected tag create message: ok=%v msg=%q", ok, ann.Message)
	}
}

func TestExtractAnnouncement_Create_RefTypeRepositoryIgnored(t *testing.T) {
	ev := rawEvent("9c", "CreateEvent", "erin", "owner/repo", `{"ref_type": "repository", "ref": ""}`)
	if _, ok := ExtractAnnouncement(ev, RepoMeta{}); ok {
		t.Fatal("expected ref_type repository (repo creation itself) to be skipped")
	}
}

func TestExtractAnnouncement_Delete_Branch(t *testing.T) {
	ev := rawEvent("10", "DeleteEvent", "frank", "owner/repo", `{"ref_type": "branch", "ref": "old-feature"}`)
	ann, ok := ExtractAnnouncement(ev, RepoMeta{})
	if !ok {
		t.Fatal("expected branch deletion to be announced")
	}
	if ann.RefID != "old-feature" || !strings.Contains(ann.Message, "[BRANCH old-feature]") ||
		!strings.Contains(ann.Message, "deleted") {
		t.Fatalf("unexpected delete message: %q", ann.Message)
	}
	if strings.Contains(ann.Message, "🔗") || ann.Link != "" {
		t.Fatalf("expected no link on a delete announcement, got message %q link %q", ann.Message, ann.Link)
	}
}

func TestExtractAnnouncement_Delete_Tag(t *testing.T) {
	ev := rawEvent("10b", "DeleteEvent", "frank", "owner/repo", `{"ref_type": "tag", "ref": "v0.9-beta"}`)
	ann, ok := ExtractAnnouncement(ev, RepoMeta{})
	if !ok || !strings.Contains(ann.Message, "[TAG v0.9-beta]") {
		t.Fatalf("unexpected tag delete message: ok=%v msg=%q", ok, ann.Message)
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
