package web

import (
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"botIAask/config"
	"botIAask/internal/guard"
	"botIAask/rss"
	"botIAask/shortlinks"
)

// initShortLinks opens the links DB and installs the "self" shortener. A DB failure only disables
// the private shortener (the public ones still work).
func (s *Server) initShortLinks() {
	st, err := shortlinks.Open("data/shortlinks.db")
	if err != nil {
		log.Printf("Warning: private URL shortener unavailable: %v", err)
		return
	}
	s.shortlinks = st
	rss.SetSelfShortener(s.shortenSelf)
	// ponytail: never stopped (lives as long as the process); add a stop channel if Server gains a Close.
	guard.Go("shortlinks-purge", func() {
		for {
			if age := s.getConfig().ShortLinks.ExpireAfter(); age > 0 {
				if n, err := st.Purge(age); err != nil {
					log.Printf("shortlinks purge: %v", err)
				} else if n > 0 {
					log.Printf("shortlinks purge: removed %d expired links", n)
				}
			}
			time.Sleep(24 * time.Hour)
		}
	})
}

// shortLinkBase is web.base_url, with the scheme forced to https when the dashboard toggle asks for it.
func shortLinkBase(cfg *config.Config) string {
	u, err := url.Parse(strings.TrimRight(cfg.Web.BaseURL, "/"))
	if err != nil || u.Host == "" {
		return ""
	}
	if cfg.ShortLinks.HTTPS {
		u.Scheme = "https"
	}
	return strings.TrimRight(u.String(), "/")
}

func (s *Server) shortenSelf(longURL string) (string, error) {
	cfg := s.getConfig()
	if s.shortlinks == nil || !cfg.ShortLinks.Enabled {
		return "", rss.ErrSelfDisabled
	}
	base := shortLinkBase(cfg)
	if base == "" {
		return "", rss.ErrSelfDisabled // web.base_url unset: nothing to build a link from
	}
	code, err := s.shortlinks.Shorten(longURL)
	if err != nil {
		return "", err
	}
	return base + "/r/" + code, nil
}

// handleShortRedirect is public (no auth) and works even when the private shortener is switched off,
// so links already announced keep resolving.
func (s *Server) handleShortRedirect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	code := strings.TrimPrefix(r.URL.Path, "/r/")
	if s.shortlinks == nil || !shortlinks.ValidCode(code) {
		http.NotFound(w, r)
		return
	}
	target, ok := s.shortlinks.Lookup(code)
	if !ok {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, target, http.StatusFound)
}
