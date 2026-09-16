package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestApplyGitHubTrackerDefaults_SetsIntervalWhenZero(t *testing.T) {
	cfg := &Config{}
	applyGitHubTrackerDefaults(cfg)
	if cfg.GitHubTracker.IntervalMinutes != 10 {
		t.Fatalf("expected default interval 10, got %d", cfg.GitHubTracker.IntervalMinutes)
	}

	cfg2 := &Config{GitHubTracker: GitHubTrackerConfig{IntervalMinutes: 5}}
	applyGitHubTrackerDefaults(cfg2)
	if cfg2.GitHubTracker.IntervalMinutes != 5 {
		t.Fatalf("expected existing interval 5 preserved, got %d", cfg2.GitHubTracker.IntervalMinutes)
	}
}

func TestValidateGitHubTracker_RejectsDuplicateOwnerRepo(t *testing.T) {
	cfg := GitHubTrackerConfig{
		Repos: []GitHubTrackerRepoConfig{
			{Owner: "foo", Repo: "bar"},
			{Owner: "Foo", Repo: "Bar"},
		},
	}
	if err := validateGitHubTracker(cfg); err == nil {
		t.Fatal("expected duplicate owner/repo to be rejected")
	}
}

func TestValidateGitHubTracker_RejectsInvalidEventType(t *testing.T) {
	cfg := GitHubTrackerConfig{
		Repos: []GitHubTrackerRepoConfig{
			{Owner: "foo", Repo: "bar", EventTypes: []string{"push", "bogus"}},
		},
	}
	if err := validateGitHubTracker(cfg); err == nil {
		t.Fatal("expected invalid event_type to be rejected")
	}
}

func TestValidateGitHubTracker_RequiresPositiveIntervalWhenEnabled(t *testing.T) {
	cfg := GitHubTrackerConfig{Enabled: true, IntervalMinutes: 0}
	if err := validateGitHubTracker(cfg); err == nil {
		t.Fatal("expected zero interval while enabled to be rejected")
	}
}

func TestFindGitHubTrackerRepo(t *testing.T) {
	repos := []GitHubTrackerRepoConfig{
		{Owner: "foo", Repo: "bar"},
		{Owner: "baz", Repo: "qux"},
	}
	got, idx, ok := FindGitHubTrackerRepo(repos, "BAZ", "QUX")
	if !ok || idx != 1 || got.Owner != "baz" {
		t.Fatalf("expected case-insensitive match at index 1, got %+v idx=%d ok=%v", got, idx, ok)
	}
	if _, _, ok := FindGitHubTrackerRepo(repos, "nope", "nope"); ok {
		t.Fatal("expected no match for absent repo")
	}
}

func TestGitHubTrackerConfig_YAMLRoundTrip(t *testing.T) {
	cfg := &Config{
		IRC: IRCConfig{Networks: []IRCNetworkConfig{{Name: "libera", Server: "irc.libera.chat"}}},
		GitHubTracker: GitHubTrackerConfig{
			Enabled:         true,
			IntervalMinutes: 15,
			Repos: []GitHubTrackerRepoConfig{
				{
					Owner:             "anthropics",
					Repo:              "claude-code",
					Channels:          []string{"libera:#dev"},
					EventTypes:        []string{"push", "release"},
					TokenEncrypted:    "opaque-ciphertext-blob",
					CachedDescription: "An agentic coding tool",
				},
			},
		},
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Config
	if err := yaml.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.GitHubTracker.Repos) != 1 {
		t.Fatalf("expected 1 repo round-tripped, got %d", len(got.GitHubTracker.Repos))
	}
	r := got.GitHubTracker.Repos[0]
	if r.TokenEncrypted != "opaque-ciphertext-blob" {
		t.Fatalf("expected TokenEncrypted to round-trip as opaque string, got %q", r.TokenEncrypted)
	}
	if r.FullName() != "anthropics/claude-code" {
		t.Fatalf("unexpected FullName: %q", r.FullName())
	}
}
