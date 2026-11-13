package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"botIAask/config"
	"botIAask/irc"
	"botIAask/reddit"
)

func (s *Server) redditFetcher() *reddit.Fetcher {
	if s.bot == nil {
		return nil
	}
	return s.bot.RedditFetcher()
}

func (s *Server) redditEnabled() bool {
	f := s.redditFetcher()
	return f != nil && f.IsEnabled()
}

// handleRedditToggle flips reddit.enabled (persisted) and applies it.
func (s *Server) handleRedditToggle(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	enabled := !s.getConfig().Reddit.Enabled
	if !s.mutateConfig(w, func(cfg *config.Config) (int, error) {
		cfg.Reddit.Enabled = enabled
		return 0, nil
	}) {
		return
	}
	s.applyReddit(w, "web (reddit toggle)", map[string]bool{"reddit_enabled": enabled})
}

// applyReddit rehashes (the fetcher picks up the new config) and writes resp.
func (s *Server) applyReddit(w http.ResponseWriter, source string, resp any) {
	if err := s.runFullRehashFromWeb(source); err != nil {
		http.Error(w, "Saved but failed to apply: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// redditTargets lists every configured "network:#chan", the choices offered per subreddit.
func redditTargets(cfg *config.Config) []string {
	out := []string{}
	for _, n := range cfg.IRC.Networks {
		for _, ch := range n.Channels {
			out = append(out, config.JoinNetworkChannel(n.Name, ch.Name))
		}
	}
	return out
}

// handleReddit: GET state, POST settings {enabled, interval_minutes}.
func (s *Server) handleReddit(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		cfg := s.getConfig()
		status := []reddit.SubStatus{}
		if f := s.redditFetcher(); f != nil {
			status = f.Statuses()
		}
		subs := cfg.Reddit.Subreddits
		if subs == nil {
			subs = []config.RedditSub{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"enabled": cfg.Reddit.Enabled, "interval_minutes": cfg.Reddit.IntervalMinutes,
			"subreddits": subs, "status": status, "targets": redditTargets(cfg),
		})
	case http.MethodPost:
		var req struct {
			Enabled         bool `json:"enabled"`
			IntervalMinutes int  `json:"interval_minutes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		if !s.mutateConfig(w, func(cfg *config.Config) (int, error) {
			cfg.Reddit.Enabled = req.Enabled
			cfg.Reddit.IntervalMinutes = req.IntervalMinutes
			return 0, nil
		}) {
			return
		}
		s.applyReddit(w, "web (reddit settings)", map[string]bool{"ok": true})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleRedditSubs: POST adds {name, channels, nsfw}, PATCH edits {name, channels?, nsfw?},
// DELETE ?name= removes. Every edit is validated (config.validateReddit) by mutateConfig's save.
func (s *Server) handleRedditSubs(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var req struct {
		Name     string    `json:"name"`
		Channels *[]string `json:"channels"`
		NSFW     *bool     `json:"nsfw"`
	}
	if r.Method == http.MethodDelete {
		req.Name = r.URL.Query().Get("name")
	} else if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	name, ok := config.NormalizeRedditName(req.Name)
	if !ok {
		http.Error(w, "Invalid subreddit name (use the bare name, e.g. golang)", http.StatusBadRequest)
		return
	}
	nsfw := req.NSFW != nil && *req.NSFW
	if r.Method == http.MethodPost || (r.Method == http.MethodPatch && req.NSFW != nil) {
		if err := irc.AllowRedditSub(s.redditFetcher(), name, nsfw); err != nil {
			http.Error(w, err.Error()+" (tick NSFW).", http.StatusBadRequest)
			return
		}
	}
	var channels []string
	if req.Channels != nil {
		channels = slices.DeleteFunc(slices.Clone(*req.Channels), func(c string) bool { return strings.TrimSpace(c) == "" })
	}

	var fn func(cfg *config.Config) (int, error)
	switch r.Method {
	case http.MethodPost:
		fn = func(cfg *config.Config) (int, error) {
			if config.FindRedditSub(cfg.Reddit.Subreddits, name) >= 0 {
				return http.StatusConflict, errors.New("Subreddit already followed")
			}
			cfg.Reddit.Subreddits = append(slices.Clone(cfg.Reddit.Subreddits), config.RedditSub{Name: name, Channels: channels, NSFW: nsfw})
			return 0, nil
		}
	case http.MethodPatch, http.MethodPut:
		fn = func(cfg *config.Config) (int, error) {
			i := config.FindRedditSub(cfg.Reddit.Subreddits, name)
			if i < 0 {
				return http.StatusNotFound, errors.New("Subreddit not followed")
			}
			cfg.Reddit.Subreddits = slices.Clone(cfg.Reddit.Subreddits)
			if req.Channels != nil {
				cfg.Reddit.Subreddits[i].Channels = channels
			}
			if req.NSFW != nil {
				cfg.Reddit.Subreddits[i].NSFW = nsfw
			}
			return 0, nil
		}
	case http.MethodDelete:
		fn = func(cfg *config.Config) (int, error) {
			i := config.FindRedditSub(cfg.Reddit.Subreddits, name)
			if i < 0 {
				return http.StatusNotFound, errors.New("Subreddit not followed")
			}
			cfg.Reddit.Subreddits = slices.Delete(slices.Clone(cfg.Reddit.Subreddits), i, i+1)
			return 0, nil
		}
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.mutateConfig(w, fn) {
		return
	}
	if len(channels) > 0 {
		s.joinAnnounceChannels(channels)
	}
	s.applyReddit(w, "web (reddit subreddits)", map[string]bool{"ok": true})
}
