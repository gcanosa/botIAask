package web

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"slices"
	"strings"

	"botIAask/config"
	"botIAask/github"
)

// joinAnnounceChannels best-effort live-joins each "network:#chan" target for the current
// process only (not persisted to config), so a freshly tracked repo's channel starts
// receiving announcements immediately instead of silently going nowhere until the admin
// separately configures autojoin or runs !join. Mirrors the same session-join call the web
// dashboard's IRC channel UI already uses (irc.Bot.JoinChannelSession). A channel that's
// already autojoined, already session-joined, or on an unreachable network just returns a
// (logged, non-fatal) error here — the repo save itself must not fail because of it.
func (s *Server) joinAnnounceChannels(channels []string) {
	if s.bot == nil {
		return
	}
	defaultNet := config.DefaultRSSNetwork(s.getConfig().IRC.Networks)
	for _, raw := range channels {
		network, ch := config.SplitNetworkChannel(raw, defaultNet)
		ch = strings.TrimSpace(ch)
		if network == "" || ch == "" {
			continue
		}
		if err := s.bot.JoinChannelSession(network, config.IRChannel{Name: ch}); err != nil {
			log.Printf("github tracker: join %s on %s: %v", ch, network, err)
		}
	}
}

// handleGitHubTrackerSettings: GET returns enabled/interval + per-repo poll status; POST
// updates enabled/interval. Mirrors handleRSSSettings.
func (s *Server) handleGitHubTrackerSettings(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}

	if r.Method == http.MethodGet {
		response := map[string]interface{}{
			"enabled":          s.getConfig().GitHubTracker.Enabled,
			"interval_minutes": s.getConfig().GitHubTracker.IntervalMinutes,
		}
		if s.githubFetcher != nil {
			response["repo_status"] = s.githubFetcher.RepoStatuses()
		} else {
			response["repo_status"] = []github.RepoStatus{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
		return
	}

	if r.Method == http.MethodPost {
		var req struct {
			Enabled         bool `json:"enabled"`
			IntervalMinutes int  `json:"interval_minutes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		if req.Enabled && req.IntervalMinutes <= 0 {
			http.Error(w, "interval_minutes must be positive when enabled", http.StatusBadRequest)
			return
		}

		var oldEnabled bool
		var oldInterval int
		if !s.mutateConfig(w, func(cfg *config.Config) (int, error) {
			oldEnabled, oldInterval = cfg.GitHubTracker.Enabled, cfg.GitHubTracker.IntervalMinutes
			cfg.GitHubTracker.Enabled = req.Enabled
			cfg.GitHubTracker.IntervalMinutes = req.IntervalMinutes
			return 0, nil
		}) {
			return
		}
		cfg := s.getConfig()

		if oldEnabled != req.Enabled || oldInterval != req.IntervalMinutes {
			s.githubFetcher.ApplyConfig(cfg)
		} else {
			s.githubFetcher.SetConfig(cfg)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]bool{"success": true})
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

// githubRepoRow is the API/dashboard view of one tracked repo. The token/ciphertext are
// never included — only HasToken, mirroring how IRC network rows never echo SASL passwords.
type githubRepoRow struct {
	Owner             string   `json:"owner"`
	Repo              string   `json:"repo"`
	Channels          []string `json:"channels"`
	EventTypes        []string `json:"event_types,omitempty"`
	HasToken          bool     `json:"has_token"`
	CachedDescription string   `json:"cached_description,omitempty"`
	OK                bool     `json:"ok"`
	Error             string   `json:"error,omitempty"`
}

func githubRepoRowFrom(r config.GitHubTrackerRepoConfig, status map[string]github.RepoStatus) githubRepoRow {
	row := githubRepoRow{
		Owner:             r.Owner,
		Repo:              r.Repo,
		Channels:          r.Channels,
		EventTypes:        r.EventTypes,
		HasToken:          r.TokenEncrypted != "",
		CachedDescription: r.CachedDescription,
	}
	if st, ok := status[strings.ToLower(r.FullName())]; ok {
		row.OK = st.OK
		row.Error = st.Error
	}
	return row
}

// handleGitHubTrackerRepos: GET lists tracked repos, POST adds one, DELETE removes one by
// ?owner=&repo=. Mirrors handleIRCNetworks.
func (s *Server) handleGitHubTrackerRepos(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}

	switch r.Method {
	case http.MethodGet:
		cfg := s.getConfig()
		status := map[string]github.RepoStatus{}
		if s.githubFetcher != nil {
			for _, st := range s.githubFetcher.RepoStatuses() {
				status[strings.ToLower(st.Repo)] = st
			}
		}
		rows := make([]githubRepoRow, 0, len(cfg.GitHubTracker.Repos))
		for _, repo := range cfg.GitHubTracker.Repos {
			rows = append(rows, githubRepoRowFrom(repo, status))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"repos": rows})
		return

	case http.MethodPost:
		var req struct {
			Owner      string   `json:"owner"`
			Repo       string   `json:"repo"`
			Channels   []string `json:"channels"`
			EventTypes []string `json:"event_types"`
			Token      string   `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		owner := strings.TrimSpace(req.Owner)
		repo := strings.TrimSpace(req.Repo)
		if owner == "" || repo == "" {
			http.Error(w, "owner and repo are required", http.StatusBadRequest)
			return
		}

		var tokenEncrypted string
		if req.Token != "" {
			enc, err := s.githubFetcher.EncryptToken(req.Token)
			if err != nil {
				http.Error(w, "Failed to encrypt token: "+err.Error(), http.StatusInternalServerError)
				return
			}
			tokenEncrypted = enc
		}

		entry := config.GitHubTrackerRepoConfig{
			Owner: owner, Repo: repo, Channels: req.Channels, EventTypes: req.EventTypes,
			TokenEncrypted: tokenEncrypted,
		}

		if !s.mutateConfig(w, func(cfg *config.Config) (int, error) {
			if _, _, exists := config.FindGitHubTrackerRepo(cfg.GitHubTracker.Repos, owner, repo); exists {
				return http.StatusConflict, errors.New("Repo already tracked")
			}
			newRepos := append(slices.Clone(cfg.GitHubTracker.Repos), entry)
			if err := config.ValidateConfig(&config.Config{IRC: cfg.IRC, GitHubTracker: config.GitHubTrackerConfig{
				Enabled: cfg.GitHubTracker.Enabled, IntervalMinutes: cfg.GitHubTracker.IntervalMinutes, Repos: newRepos,
			}}); err != nil {
				return http.StatusBadRequest, err
			}
			cfg.GitHubTracker.Repos = newRepos
			return 0, nil
		}) {
			return
		}

		if err := s.runFullRehashFromWeb("web (github repos add)"); err != nil {
			http.Error(w, "Saved but failed to apply: "+err.Error(), http.StatusInternalServerError)
			return
		}
		s.joinAnnounceChannels(req.Channels)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		return

	case http.MethodDelete:
		owner := strings.TrimSpace(r.URL.Query().Get("owner"))
		repo := strings.TrimSpace(r.URL.Query().Get("repo"))
		if owner == "" || repo == "" {
			http.Error(w, "Missing owner/repo query", http.StatusBadRequest)
			return
		}
		if !s.mutateConfig(w, func(cfg *config.Config) (int, error) {
			if _, _, ok := config.FindGitHubTrackerRepo(cfg.GitHubTracker.Repos, owner, repo); !ok {
				return http.StatusNotFound, errors.New("Repo not found")
			}
			cfg.GitHubTracker.Repos = slices.DeleteFunc(slices.Clone(cfg.GitHubTracker.Repos), func(r config.GitHubTrackerRepoConfig) bool {
				return strings.EqualFold(r.Owner, owner) && strings.EqualFold(r.Repo, repo)
			})
			return 0, nil
		}) {
			return
		}

		if err := s.runFullRehashFromWeb("web (github repos remove)"); err != nil {
			http.Error(w, "Saved but failed to apply: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		return

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleGitHubTrackerRepoEdit updates an existing tracked repo's channels/event filter/
// token; owner/repo are immutable here (rename = delete+add, same convention as IRC
// network names). A blank/omitted token keeps the existing encrypted value unchanged.
func (s *Server) handleGitHubTrackerRepoEdit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut && r.Method != http.MethodPatch {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireAdmin(w, r) {
		return
	}

	var req struct {
		Owner      string    `json:"owner"`
		Repo       string    `json:"repo"`
		Channels   *[]string `json:"channels"`
		EventTypes *[]string `json:"event_types"`
		Token      *string   `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	owner := strings.TrimSpace(req.Owner)
	repo := strings.TrimSpace(req.Repo)
	if owner == "" || repo == "" {
		http.Error(w, "owner and repo are required", http.StatusBadRequest)
		return
	}

	if !s.mutateConfig(w, func(cfg *config.Config) (int, error) {
		_, idx, ok := config.FindGitHubTrackerRepo(cfg.GitHubTracker.Repos, owner, repo)
		if !ok {
			return http.StatusNotFound, errors.New("Repo not found")
		}
		repoCfg := &cfg.GitHubTracker.Repos[idx]
		if req.Channels != nil {
			repoCfg.Channels = *req.Channels
		}
		if req.EventTypes != nil {
			repoCfg.EventTypes = *req.EventTypes
		}
		// req.Token nil means "leave the stored token as-is" (the field was omitted); a
		// non-nil empty string is an explicit clear (repo becomes public/unauthenticated);
		// a non-nil non-empty string replaces the stored ciphertext.
		if req.Token != nil {
			if *req.Token == "" {
				repoCfg.TokenEncrypted = ""
			} else {
				enc, err := s.githubFetcher.EncryptToken(*req.Token)
				if err != nil {
					return http.StatusInternalServerError, errors.New("Failed to encrypt token: " + err.Error())
				}
				repoCfg.TokenEncrypted = enc
			}
		}
		return 0, nil
	}) {
		return
	}
	if err := s.runFullRehashFromWeb("web (github repos edit)"); err != nil {
		http.Error(w, "Saved but failed to apply: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if req.Channels != nil {
		s.joinAnnounceChannels(*req.Channels)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}
