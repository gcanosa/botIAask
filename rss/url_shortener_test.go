package rss

import (
	"strings"
	"testing"
)

func TestAvailableShorteners(t *testing.T) {
	shorteners := AvailableShorteners()
	if len(shorteners) == 0 {
		t.Fatal("Expected at least one shortener service")
	}
	if !contains(shorteners, "is.gd") {
		t.Error("Expected is.gd in available shorteners")
	}
	if !contains(shorteners, "tinyurl") {
		t.Error("Expected tinyurl in available shorteners")
	}
	if !contains(shorteners, "v.gd") {
		t.Error("Expected v.gd in available shorteners")
	}
}

func TestShortenURLEmpty(t *testing.T) {
	result := ShortenURL("")
	if result != "" {
		t.Errorf("Expected empty string for empty URL, got %s", result)
	}
}

func TestShortenURLWithServiceEmpty(t *testing.T) {
	result := ShortenURLWithService("", "is.gd")
	if result != "" {
		t.Errorf("Expected empty string for empty URL, got %s", result)
	}
}

func TestShortenURLWithUnknownService(t *testing.T) {
	longURL := "https://example.com/very/long/url/that/needs/shortening"
	result := ShortenURLWithService(longURL, "unknown_service")
	if result == "" {
		t.Error("Expected non-empty result with fallback chain")
	}
	if result == longURL {
		t.Log("Warning: All shorteners failed, returned long URL")
	}
}

func contains(slice []string, item string) bool {
	for _, v := range slice {
		if v == item {
			return true
		}
	}
	return false
}

func TestShortenersHaveValidNames(t *testing.T) {
	services := []struct {
		name string
		fn   func(string) (string, error)
	}{
		{"is.gd", shortenWithIsGd},
		{"tinyurl", shortenWithTinyURL},
		{"v.gd", shortenWithVGd},
	}

	for _, svc := range services {
		if strings.TrimSpace(svc.name) == "" {
			t.Errorf("Service has empty name")
		}
	}
}
