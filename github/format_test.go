package github

import (
	"strings"
	"testing"
)

func TestFormatPush_ShowsShortSHAOnce(t *testing.T) {
	msg := formatPush("owner/repo", "alice", "main", 1, "", "", "https://github.com/owner/repo/commit/abcd1234", "abcd123")
	if !strings.Contains(msg, "[PUSH]") || strings.Count(msg, "abcd123") != 2 { // body + link URL
		t.Fatalf("expected plain PUSH tag and SHA in body, got %q", msg)
	}
}

func TestFormatPullRequest_TagCarriesNumber(t *testing.T) {
	msg := formatPullRequest("owner/repo", "bob", "opened", "#42", "Fix bug", "https://github.com/owner/repo/pull/42")
	if !strings.Contains(msg, "[PR #42]") {
		t.Fatalf("expected tag with PR number, got %q", msg)
	}
}

func TestFormatRelease_OmitsNameWhenSameAsTag(t *testing.T) {
	msg := formatRelease("owner/repo", "carol", "v1.0.0", "v1.0.0", "https://github.com/owner/repo/releases/tag/v1.0.0")
	if !strings.Contains(msg, "[RELEASE v1.0.0]") {
		t.Fatalf("expected tag with release tag, got %q", msg)
	}
	if strings.Count(msg, "v1.0.0") != 2 { // once in the tag, once in the link URL — not repeated a third time in body
		t.Fatalf("expected release name not repeated when identical to tag: %q", msg)
	}
}

func TestFormatRelease_ShowsNameWhenDifferentFromTag(t *testing.T) {
	msg := formatRelease("owner/repo", "carol", "v1.0.0", "First stable release", "https://github.com/owner/repo/releases/tag/v1.0.0")
	if !strings.Contains(msg, `"First stable release"`) {
		t.Fatalf("expected release name shown when different from tag, got %q", msg)
	}
}

func TestFormatIssue_TagCarriesNumber(t *testing.T) {
	msg := formatIssue("owner/repo", "dave", "opened", "#45", "Something broke", "https://github.com/owner/repo/issues/45")
	if !strings.Contains(msg, "[ISSUE #45]") {
		t.Fatalf("expected tag with issue number, got %q", msg)
	}
}

func TestFormatCreate_TagLabelMatchesRefType(t *testing.T) {
	branch := formatCreate("owner/repo", "erin", "branch", "feature/x", "https://github.com/owner/repo/tree/feature/x")
	if !strings.Contains(branch, "[BRANCH feature/x]") {
		t.Fatalf("expected BRANCH tag, got %q", branch)
	}
	tag := formatCreate("owner/repo", "erin", "tag", "v2.0", "https://github.com/owner/repo/tree/v2.0")
	if !strings.Contains(tag, "[TAG v2.0]") {
		t.Fatalf("expected TAG tag, got %q", tag)
	}
}

func TestFormatDelete_NoLinkSuffix(t *testing.T) {
	msg := formatDelete("owner/repo", "frank", "branch", "old-feature")
	if !strings.Contains(msg, "[BRANCH old-feature]") || !strings.Contains(msg, "deleted") {
		t.Fatalf("unexpected delete message: %q", msg)
	}
	if strings.Contains(msg, "🔗") {
		t.Fatalf("expected no link suffix in a delete message, got %q", msg)
	}
}

func TestShortenAnnouncementLink_NoLinkReturnsMessageUnchanged(t *testing.T) {
	ann := Announcement{Message: "no link here"}
	if got := shortenAnnouncementLink(ann, ""); got != ann.Message {
		t.Fatalf("expected unchanged message, got %q", got)
	}
}

func TestShortenAnnouncementLink_ReplacesLinkInMessage(t *testing.T) {
	oldFn := shortenURLFunc
	shortenURLFunc = func(link, _ string) string { return "https://short/abc" }
	t.Cleanup(func() { shortenURLFunc = oldFn })

	ann := Announcement{
		Message: "pushed to main 🔗 https://github.com/owner/repo/commit/abcd1234",
		Link:    "https://github.com/owner/repo/commit/abcd1234",
	}
	got := shortenAnnouncementLink(ann, "")
	if strings.Contains(got, ann.Link) {
		t.Fatalf("expected long link replaced, got %q", got)
	}
	if !strings.Contains(got, "https://short/abc") {
		t.Fatalf("expected shortened link present, got %q", got)
	}
}
