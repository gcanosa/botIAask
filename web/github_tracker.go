package web

import (
	"encoding/json"
	"net/http"
	"strings"

	"botIAask/config"
	"botIAask/github"
)

// handleGitHubTrackerSettings: GET returns enabled/interval + per-repo poll status; POST
// updates enabled/interval. Mirrors handleRSSSettings.
func (s *Server) handleGitHubTrackerSettings(w http.ResponseWriter, r *http.Request) {
	isAdmin, _ := s.requireAdminCSRF(r)
	if !isAdmin {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
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

		s.cfgMu.Lock()
		oldEnabled := s.cfg.GitHubTracker.Enabled
		oldInterval := s.cfg.GitHubTracker.IntervalMinutes
		s.cfg.GitHubTracker.Enabled = req.Enabled
		s.cfg.GitHubTracker.IntervalMinutes = req.IntervalMinutes
		if err := config.SaveConfig(config.DefaultConfigPath, s.cfg); err != nil {
			s.cfgMu.Unlock()
			http.Error(w, "Failed to save config: "+err.Error(), http.StatusInternalServerError)
			return
		}
		cfg := s.cfg
		s.cfgMu.Unlock()

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
	isAdmin, _ := s.requireAdminCSRF(r)
	if !isAdmin {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
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

		s.cfgMu.Lock()
		if _, _, exists := config.FindGitHubTrackerRepo(s.cfg.GitHubTracker.Repos, owner, repo); exists {
			s.cfgMu.Unlock()
			http.Error(w, "Repo already tracked", http.StatusConflict)
			return
		}
		newRepos := append(append([]config.GitHubTrackerRepoConfig(nil), s.cfg.GitHubTracker.Repos...), entry)
		if err := config.ValidateConfig(&config.Config{IRC: s.cfg.IRC, GitHubTracker: config.GitHubTrackerConfig{
			Enabled: s.cfg.GitHubTracker.Enabled, IntervalMinutes: s.cfg.GitHubTracker.IntervalMinutes, Repos: newRepos,
		}}); err != nil {
			s.cfgMu.Unlock()
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.cfg.GitHubTracker.Repos = newRepos
		if err := config.SaveConfig(config.DefaultConfigPath, s.cfg); err != nil {
			s.cfgMu.Unlock()
			http.Error(w, "Failed to save config: "+err.Error(), http.StatusInternalServerError)
			return
		}
		s.cfgMu.Unlock()

		if err := s.runFullRehashFromWeb("web (github repos add)"); err != nil {
			http.Error(w, "Saved but failed to apply: "+err.Error(), http.StatusInternalServerError)
			return
		}
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
		s.cfgMu.Lock()
		if _, _, ok := config.FindGitHubTrackerRepo(s.cfg.GitHubTracker.Repos, owner, repo); !ok {
			s.cfgMu.Unlock()
			http.Error(w, "Repo not found", http.StatusNotFound)
			return
		}
		var out []config.GitHubTrackerRepoConfig
		for _, repoCfg := range s.cfg.GitHubTracker.Repos {
			if !(strings.EqualFold(repoCfg.Owner, owner) && strings.EqualFold(repoCfg.Repo, repo)) {
				out = append(out, repoCfg)
			}
		}
		s.cfg.GitHubTracker.Repos = out
		if err := config.SaveConfig(config.DefaultConfigPath, s.cfg); err != nil {
			s.cfgMu.Unlock()
			http.Error(w, "Failed to save config: "+err.Error(), http.StatusInternalServerError)
			return
		}
		s.cfgMu.Unlock()

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
	isAdmin, _ := s.requireAdminCSRF(r)
	if !isAdmin {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
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

	s.cfgMu.Lock()
	_, idx, ok := config.FindGitHubTrackerRepo(s.cfg.GitHubTracker.Repos, owner, repo)
	if !ok {
		s.cfgMu.Unlock()
		http.Error(w, "Repo not found", http.StatusNotFound)
		return
	}
	if req.Channels != nil {
		s.cfg.GitHubTracker.Repos[idx].Channels = *req.Channels
	}
	if req.EventTypes != nil {
		s.cfg.GitHubTracker.Repos[idx].EventTypes = *req.EventTypes
	}
	// req.Token nil means "leave the stored token as-is" (the field was omitted); a
	// non-nil empty string is an explicit clear (repo becomes public/unauthenticated);
	// a non-nil non-empty string replaces the stored ciphertext.
	if req.Token != nil {
		if *req.Token == "" {
			s.cfg.GitHubTracker.Repos[idx].TokenEncrypted = ""
		} else {
			enc, err := s.githubFetcher.EncryptToken(*req.Token)
			if err != nil {
				s.cfgMu.Unlock()
				http.Error(w, "Failed to encrypt token: "+err.Error(), http.StatusInternalServerError)
				return
			}
			s.cfg.GitHubTracker.Repos[idx].TokenEncrypted = enc
		}
	}
	if err := config.SaveConfig(config.DefaultConfigPath, s.cfg); err != nil {
		s.cfgMu.Unlock()
		http.Error(w, "Failed to save config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.cfgMu.Unlock()

	if err := s.runFullRehashFromWeb("web (github repos edit)"); err != nil {
		http.Error(w, "Saved but failed to apply: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}
