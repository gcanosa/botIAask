package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"botIAask/config"
	"botIAask/logger"
	"botIAask/rss"
)

const (
	weatherSettingsMin = 1
	weatherSettingsMax = 10080
)

func (s *Server) handleWeatherSettings(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}

	if r.Method == http.MethodGet {
		m := s.getConfig().Web.WeatherRefreshMinutes
		if m < 1 {
			m = 1
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int{"weather_refresh_minutes": m})
		return
	}

	if r.Method == http.MethodPost {
		var req struct {
			WeatherRefreshMinutes int `json:"weather_refresh_minutes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		if req.WeatherRefreshMinutes < weatherSettingsMin || req.WeatherRefreshMinutes > weatherSettingsMax {
			http.Error(w, fmt.Sprintf("weather_refresh_minutes must be between %d and %d", weatherSettingsMin, weatherSettingsMax), http.StatusBadRequest)
			return
		}

		if !s.mutateConfig(w, func(cfg *config.Config) (int, error) {
			cfg.Web.WeatherRefreshMinutes = req.WeatherRefreshMinutes
			return 0, nil
		}) {
			return
		}
		s.clearWeatherCache()
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]bool{"success": true})
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

const (
	loggerSettingsMin = 0
	loggerSettingsMax = 3650
)

func loggerRetentionToDays(value int, unit string) (int, error) {
	if value < 0 {
		return 0, fmt.Errorf("retention value must be non-negative")
	}
	switch strings.ToLower(strings.TrimSpace(unit)) {
	case "", "days":
		if value > loggerSettingsMax {
			return 0, fmt.Errorf("rotation_days must be between %d and %d", loggerSettingsMin, loggerSettingsMax)
		}
		return value, nil
	case "months":
		d := value * 30
		if d > loggerSettingsMax {
			return 0, fmt.Errorf("retention exceeds %d days", loggerSettingsMax)
		}
		return d, nil
	case "years":
		d := value * 365
		if d > loggerSettingsMax {
			return 0, fmt.Errorf("retention exceeds %d days", loggerSettingsMax)
		}
		return d, nil
	default:
		return 0, fmt.Errorf("retention_unit must be days, months, or years")
	}
}

func (s *Server) handleLoggerSettings(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}

	if r.Method == http.MethodGet {
		d := s.getConfig().Logger.RotationDays
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int{"rotation_days": d})
		return
	}

	if r.Method == http.MethodPost {
		var req struct {
			RotationDays   *int   `json:"rotation_days"`
			RetentionValue *int   `json:"retention_value"`
			RetentionUnit  string `json:"retention_unit"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		var days int
		var err error
		if req.RetentionValue != nil {
			unit := req.RetentionUnit
			if unit == "" {
				unit = "days"
			}
			days, err = loggerRetentionToDays(*req.RetentionValue, unit)
		} else if req.RotationDays != nil {
			days = *req.RotationDays
			if days < loggerSettingsMin || days > loggerSettingsMax {
				err = fmt.Errorf("rotation_days must be between %d and %d", loggerSettingsMin, loggerSettingsMax)
			}
		} else {
			http.Error(w, "rotation_days or retention_value required", http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if !s.mutateConfig(w, func(cfg *config.Config) (int, error) {
			cfg.Logger.RotationDays = days
			return 0, nil
		}) {
			return
		}
		logger.SetRotationDays(days)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "rotation_days": days})
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

const (
	aiEndpointMaxLen = 2048
	aiModelMaxLen    = 512
)

func validateLMStudioURL(raw string) (string, error) {
	u := strings.TrimSpace(raw)
	if u == "" {
		return "", fmt.Errorf("lm_studio_url is required")
	}
	if len(u) > aiEndpointMaxLen {
		return "", fmt.Errorf("lm_studio_url exceeds %d characters", aiEndpointMaxLen)
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return "", fmt.Errorf("invalid lm_studio_url")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("lm_studio_url must use http or https")
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("lm_studio_url must include a host")
	}
	return u, nil
}

func validateAIModel(raw string) (string, error) {
	m := strings.TrimSpace(raw)
	if m == "" {
		return "", fmt.Errorf("model is required")
	}
	if len(m) > aiModelMaxLen {
		return "", fmt.Errorf("model exceeds %d characters", aiModelMaxLen)
	}
	return m, nil
}

func (s *Server) handleAISettings(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}

	if r.Method == http.MethodGet {
		cfg := s.getConfig()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"lm_studio_url": cfg.AI.LMStudioURL,
			"model":         cfg.AI.Model,
		})
		return
	}

	if r.Method == http.MethodPost {
		var req struct {
			LMStudioURL string `json:"lm_studio_url"`
			Model       string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		baseURL, err := validateLMStudioURL(req.LMStudioURL)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		model, err := validateAIModel(req.Model)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if !s.mutateConfig(w, func(cfg *config.Config) (int, error) {
			cfg.AI.LMStudioURL = baseURL
			cfg.AI.Model = model
			return 0, nil
		}) {
			return
		}
		if s.aiClient != nil {
			s.aiClient.UpdateConfig(baseURL, model)
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success":       true,
			"lm_studio_url": baseURL,
			"model":         model,
		})
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

func (s *Server) handleUITheme(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireAdmin(w, r) {
		return
	}
	cookie, err := r.Cookie("admin_session")
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	userID, _, err := s.authDB.ValidateSession(cookie.Value)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var body struct {
		Theme string `json:"theme"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	switch body.Theme {
	case "dark", "light", "mono":
	default:
		http.Error(w, "Invalid theme", http.StatusBadRequest)
		return
	}
	if err := s.authDB.SetUITheme(userID, body.Theme); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleRSSSettings(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}

	if r.Method == http.MethodGet {
		response := map[string]interface{}{
			"interval_minutes":     s.getConfig().RSS.IntervalMinutes,
			"retention_count":      s.getConfig().RSS.RetentionCount,
			"feed_urls":            s.getConfig().RSS.FeedURLs,
			"announce_to_irc":      s.getConfig().RSS.AnnounceToIRCEnabled(),
			"url_shortener":        s.getConfig().RSS.URLShortener,
			"available_shorteners": rss.AvailableShorteners(),
			"short_links":          s.getConfig().ShortLinks,
			"short_link_example":   shortLinkBase(s.getConfig()) + "/r/Ab3dE9x",
		}
		if s.rssFetcher != nil {
			response["feed_status"] = s.rssFetcher.FeedStatuses()
		} else {
			response["feed_status"] = []rss.FeedStatus{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
		return
	}

	if r.Method == http.MethodPost {
		var req struct {
			IntervalMinutes int                      `json:"interval_minutes"`
			RetentionCount  int                      `json:"retention_count"`
			FeedURLs        []string                 `json:"feed_urls"`
			AnnounceToIRC   *bool                    `json:"announce_to_irc,omitempty"`
			URLShortener    string                   `json:"url_shortener,omitempty"`
			ShortLinks      *config.ShortLinksConfig `json:"short_links,omitempty"`
		}

		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		if req.IntervalMinutes <= 0 {
			http.Error(w, "interval_minutes must be positive", http.StatusBadRequest)
			return
		}

		var oldInterval int
		if !s.mutateConfig(w, func(cfg *config.Config) (int, error) {
			oldInterval = cfg.RSS.IntervalMinutes
			cfg.RSS.IntervalMinutes = req.IntervalMinutes
			cfg.RSS.RetentionCount = req.RetentionCount
			cfg.RSS.FeedURLs = req.FeedURLs
			if req.AnnounceToIRC != nil {
				v := *req.AnnounceToIRC
				cfg.RSS.AnnounceToIRC = &v
			}
			if req.URLShortener != "" {
				cfg.RSS.URLShortener = req.URLShortener
			}
			if req.ShortLinks != nil {
				cfg.ShortLinks = *req.ShortLinks
			}
			return 0, nil
		}) {
			return
		}

		// Always push the updated config into the fetcher so settings like
		// URLShortener and AnnounceToIRC take effect immediately.
		if oldInterval != req.IntervalMinutes {
			// Interval change requires a full restart to reset the ticker.
			s.rssFetcher.ApplyConfig(s.getConfig())
		} else {
			// Non-structural change: swap config without restarting the loop.
			s.rssFetcher.SetConfig(s.getConfig())
		}

		// Prune per source (never the global newest-N: that would wipe other feeds' history and re-announce it).
		if req.RetentionCount > 0 {
			s.rssFetcher.GetDB().CleanupPerSource(req.RetentionCount)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]bool{"success": true})
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

func (s *Server) handleUploadSettings(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("admin_session")
	if err != nil || !s.staffAdminFromRequest(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if !s.csrfValid(r, cookie.Value) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]int{"max_file_mb": s.effectiveMaxFileMB()})
	case http.MethodPost:
		var req struct {
			MaxFileMB int `json:"max_file_mb"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		if req.MaxFileMB < 1 || req.MaxFileMB > 2048 {
			http.Error(w, "max_file_mb must be between 1 and 2048", http.StatusBadRequest)
			return
		}
		if !s.mutateConfig(w, func(cfg *config.Config) (int, error) {
			cfg.Uploads.MaxFileMB = req.MaxFileMB
			return 0, nil
		}) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"success": true})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}
