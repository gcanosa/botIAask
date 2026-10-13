package irc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLatestAppleReleases(t *testing.T) {
	r := latestAppleReleases([]string{
		"iOS 27.2 beta 3 (24B5099f)",
		"Xcode 27.1 RC (27A9275)",
		"iPadOS 27.0.1 (24A446)",
		"iPadOS 26.7.1 (23H30)",
		"macOS Tahoe 26.6.2 (25G83)",
		"macOS 27.0.1 (26A434)",
		"tvOS 27.0 (24J361)",
		"tvOS 26.6 (23L773)",
	})
	want := map[string]appleRelease{
		"iPadOS": {version: "27.0.1", build: "24A446"},
		"macOS":  {version: "27.0.1", build: "26A434"},
		"tvOS":   {version: "27.0", build: "24J361"},
	}
	if len(r) != len(want) {
		t.Fatalf("got %v", r)
	}
	for k, v := range want {
		if r[k] != v {
			t.Errorf("%s = %v, want %v", k, r[k], v)
		}
	}
}

func TestMacOSNameFromApple(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("<html><title>OS - macOS 28 Big Apple - Apple</title>"))
	}))
	defer srv.Close()
	old := appleMacOSPage
	appleMacOSPage = srv.URL
	defer func() { appleMacOSPage = old }()
	if got := macOSNameFromApple(context.Background(), "28"); got != "Big Apple" {
		t.Errorf("got %q", got)
	}
	if got := macOSNameFromApple(context.Background(), "29"); got != "" {
		t.Errorf("major mismatch should be empty, got %q", got)
	}
}
