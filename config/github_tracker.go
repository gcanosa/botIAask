package config

import "strings"

// GitHubTrackerConfig holds settings for the multi-repo GitHub activity -> IRC announcer
// (pushes/PRs/releases via the GitHub Events API). Distinct from GitHubConfig, which is
// used only by the web dashboard's self-changelog widget (single repo, commits API).
type GitHubTrackerConfig struct {
	Enabled         bool                      `yaml:"enabled"`
	IntervalMinutes int                       `yaml:"interval_minutes"`
	Repos           []GitHubTrackerRepoConfig `yaml:"repos,omitempty"`
}

// GitHubTrackerRepoConfig is one tracked repo: where to announce, which event types to
// announce, and (for private repos) an at-rest-encrypted PAT.
type GitHubTrackerRepoConfig struct {
	Owner    string   `yaml:"owner"`
	Repo     string   `yaml:"repo"`
	Channels []string `yaml:"channels,omitempty"` // "network:#chan" entries, see SplitNetworkChannel
	// EventTypes restricts which event kinds this repo announces (subset of
	// "push"/"pull_request"/"release"). Empty means all three.
	EventTypes []string `yaml:"event_types,omitempty"`
	// TokenEncrypted is the AES-256-GCM ciphertext (base64) of a GitHub PAT, produced by
	// github.Cryptor. Empty means the repo is polled unauthenticated (public repo).
	TokenEncrypted string `yaml:"token_encrypted,omitempty" json:"-"`
	// CachedDescription is the repo's GitHub description, fetched once when the repo is
	// added via the dashboard. Shown in the web UI only; never sent to IRC.
	CachedDescription string `yaml:"cached_description,omitempty"`
}

// FullName returns the "owner/repo" identifier used as the dedup/ETag storage key.
func (r GitHubTrackerRepoConfig) FullName() string {
	return r.Owner + "/" + r.Repo
}

// FindGitHubTrackerRepo returns the repo matching owner/repo (case-insensitive) and its
// index in repos, or ok=false if not found.
func FindGitHubTrackerRepo(repos []GitHubTrackerRepoConfig, owner, repo string) (cfg GitHubTrackerRepoConfig, index int, ok bool) {
	for i, r := range repos {
		if strings.EqualFold(r.Owner, owner) && strings.EqualFold(r.Repo, repo) {
			return r, i, true
		}
	}
	return GitHubTrackerRepoConfig{}, -1, false
}
