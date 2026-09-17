package github

import (
	"fmt"
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

// TestExtractAnnouncement_PushTrimmedPayload covers the shape GitHub's public Events API
// actually returns today: no "size"/"commits" fields at all.
func TestExtractAnnouncement_PushTrimmedPayload(t *testing.T) {
	ev := rawEvent("1b", "PushEvent", "alice", "owner/repo", `{
		"ref": "refs/heads/main",
		"head": "abcd1234",
		"before": "9876fedc"
	}`)
	ann, ok := ExtractAnnouncement(ev, RepoMeta{})
	if !ok {
		t.Fatal("expected trimmed push to still be announced")
	}
	if !strings.Contains(ann.Message, "alice") || !strings.Contains(ann.Message, "main") ||
		!strings.Contains(ann.Message, "compare/9876fedc...abcd1234") {
		t.Fatalf("unexpected trimmed push message: %q", ann.Message)
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

func TestCollapseForBroadcast_MultiplePushesFoldToOneLine(t *testing.T) {
	anns := []Announcement{
		{RepoFullName: "owner/repo", Kind: "push", Message: "push 1"},
		{RepoFullName: "owner/repo", Kind: "push", Message: "push 2 (latest)"},
	}
	out := CollapseForBroadcast(anns)
	if len(out) != 1 {
		t.Fatalf("expected 1 collapsed line, got %d: %v", len(out), out)
	}
	if !strings.Contains(out[0].Message, "push 2 (latest)") || !strings.Contains(out[0].Message, "+1 more pushes") {
		t.Fatalf("unexpected collapsed message: %q", out[0].Message)
	}
}

func TestCollapseForBroadcast_DifferentKindsEachGetALine(t *testing.T) {
	anns := []Announcement{
		{RepoFullName: "owner/repo", Kind: "push", Message: "push 1"},
		{RepoFullName: "owner/repo", Kind: "pull_request", Message: "pr opened"},
		{RepoFullName: "owner/repo", Kind: "pull_request", Message: "pr merged"},
	}
	out := CollapseForBroadcast(anns)
	if len(out) != 2 {
		t.Fatalf("expected 2 collapsed lines (push, pull_request), got %d: %v", len(out), out)
	}
	if out[0].Message != "push 1" {
		t.Fatalf("expected single push line unchanged, got %q", out[0].Message)
	}
	if !strings.Contains(out[1].Message, "pr merged") || !strings.Contains(out[1].Message, "+1 more PR updates") {
		t.Fatalf("unexpected pull_request line: %q", out[1].Message)
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

func TestCollapseForBroadcast_NewKindsEachGetALine(t *testing.T) {
	anns := []Announcement{
		{RepoFullName: "owner/repo", Kind: "issues", Message: "issue 1"},
		{RepoFullName: "owner/repo", Kind: "issues", Message: "issue 2 (latest)"},
		{RepoFullName: "owner/repo", Kind: "create", Message: "branch created"},
		{RepoFullName: "owner/repo", Kind: "delete", Message: "branch deleted"},
	}
	out := CollapseForBroadcast(anns)
	if len(out) != 3 {
		t.Fatalf("expected 3 collapsed lines (issues, create, delete), got %d: %v", len(out), out)
	}
	if !strings.Contains(out[0].Message, "issue 2 (latest)") || !strings.Contains(out[0].Message, "+1 more issues") {
		t.Fatalf("unexpected issues line: %q", out[0].Message)
	}
	if out[1].Message != "branch created" || out[2].Message != "branch deleted" {
		t.Fatalf("unexpected create/delete lines: %q / %q", out[1].Message, out[2].Message)
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
