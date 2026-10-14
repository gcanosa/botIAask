package web

import "testing"

// The public changelog must not hand commit author e-mail addresses to anonymous visitors.
func TestParseGitLogOmitsEmail(t *testing.T) {
	commits := parseGitLog("abc1234567|Jane Dev|jane@example.com|2026-01-02 03:04:05 +0000|fix things")
	if len(commits) != 1 {
		t.Fatalf("got %d commits", len(commits))
	}
	if _, ok := commits[0]["email"]; ok {
		t.Fatal("changelog entry exposes the author e-mail")
	}
	if commits[0]["author"] != "Jane Dev" || commits[0]["message"] != "fix things" {
		t.Fatalf("unexpected entry: %v", commits[0])
	}
}
