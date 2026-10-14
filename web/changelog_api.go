package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"botIAask/config"
)

func (s *Server) handleChangelog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}

	// Public endpoint: each miss hits the GitHub API (burning its rate limit) or spawns `git log`,
	// so serve a short-lived cached copy per limit.
	commits := s.changelogCached(limit)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"commits": commits,
		"total":   len(commits),
	})
}

const changelogTTL = 5 * time.Minute

type changelogEntry struct {
	at      time.Time
	commits []map[string]interface{}
}

func (s *Server) changelogCached(limit int) []map[string]interface{} {
	s.changelogMu.Lock()
	defer s.changelogMu.Unlock()
	if e, ok := s.changelog[limit]; ok && time.Since(e.at) < changelogTTL {
		return e.commits
	}
	cfg := s.getConfig()
	var commits []map[string]interface{}
	// Use GitHub API if configured, otherwise fall back to local git
	if cfg.GitHub.Owner != "" && cfg.GitHub.Repo != "" {
		commits = fetchFromGitHub(cfg.GitHub, limit)
	} else {
		commits = parseGitLog(executeGitLog(limit))
	}
	if s.changelog == nil {
		s.changelog = make(map[int]changelogEntry)
	}
	s.changelog[limit] = changelogEntry{at: time.Now(), commits: commits} // ponytail: limit is capped at 200 so the map stays tiny
	return commits
}

func executeGitLog(limit int) string {
	// Run git log command
	cmd := exec.CommandContext(context.Background(), "git", "log", "--pretty=format:%H|%an|%ae|%ai|%s", fmt.Sprintf("-n%d", limit))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	return string(out)
}

func parseGitLog(gitLog string) []map[string]interface{} {
	var commits []map[string]interface{}
	lines := strings.Split(strings.TrimSpace(gitLog), "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) >= 5 {
			// Parse git date format: "2026-05-29 09:16:08 +0200"
			dateStr := strings.TrimSpace(parts[3])
			commitTime, err := time.Parse("2006-01-02 15:04:05 -0700", dateStr)
			if err != nil {
				commitTime = time.Now()
			}
			shortID := parts[0]
			if len(shortID) > 7 {
				shortID = shortID[:7]
			}
			commit := map[string]interface{}{
				"id":        parts[0],
				"author":    parts[1],
				"timestamp": commitTime.Format(time.RFC3339),
				"date":      commitTime.Format("2006-01-02"),
				"time":      commitTime.Format("15:04:05"),
				"message":   parts[4],
				"shortId":   shortID,
			}
			commits = append(commits, commit)
		}
	}
	return commits
}

func fetchFromGitHub(ghCfg config.GitHubConfig, limit int) []map[string]interface{} {
	ref := ghCfg.Ref
	if ref == "" {
		ref = "main"
	}

	// GitHub's commits API caps per_page at 100.
	if limit > 100 {
		limit = 100
	}

	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits?per_page=%d&sha=%s",
		url.QueryEscape(ghCfg.Owner), url.QueryEscape(ghCfg.Repo), limit, url.QueryEscape(ref))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		log.Printf("github changelog: request creation failed: %v", err)
		return nil
	}

	req.Header.Set("User-Agent", "botIAask")
	if ghCfg.Token != "" {
		req.Header.Set("Authorization", "token "+ghCfg.Token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("github changelog: request failed: %v", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("github changelog: status %d", resp.StatusCode)
		return nil
	}

	var commits []struct {
		SHA    string `json:"sha"`
		Author struct {
			Name  string `json:"name"`
			Email string `json:"email"`
			Date  string `json:"date"`
		} `json:"author"`
		Commit struct {
			Message string `json:"message"`
			Author  struct {
				Name  string `json:"name"`
				Email string `json:"email"`
				Date  string `json:"date"`
			} `json:"author"`
		} `json:"commit"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&commits); err != nil {
		log.Printf("github changelog: decode failed: %v", err)
		return nil
	}

	var result []map[string]interface{}
	for _, c := range commits {
		if c.Commit.Author.Date == "" {
			continue
		}

		commitTime, err := time.Parse(time.RFC3339, c.Commit.Author.Date)
		if err != nil {
			continue
		}

		shortID := c.SHA
		if len(shortID) > 7 {
			shortID = shortID[:7]
		}

		commit := map[string]interface{}{
			"id":        c.SHA,
			"author":    c.Commit.Author.Name,
			"timestamp": commitTime.Format(time.RFC3339),
			"date":      commitTime.Format("2006-01-02"),
			"time":      commitTime.Format("15:04:05"),
			"message":   c.Commit.Message,
			"shortId":   shortID,
		}
		result = append(result, commit)
	}

	return result
}
