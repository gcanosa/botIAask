package irc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"botIAask/meta"
	"botIAask/rss"
)

var latestHTTP = &http.Client{Timeout: 10 * time.Second}

// Endpoints (vars so tests can point them at httptest servers).
var (
	latestGitHubAPI   = "https://api.github.com"
	latestGitLabAPI   = "https://gitlab.com/api/v4"
	latestCodebergAPI = "https://codeberg.org/api/v1"
	latestSFBase      = "https://sourceforge.net/projects/"
	latestRepology    = "https://repology.org/api/v1/project/"
)

var errLatestNotFound = errors.New("not found")

// latestQuery is a parsed !latest argument. kind is gh, gl, cb, sf or repology.
type latestQuery struct {
	kind, name string
}

var latestNameRe = regexp.MustCompile(`^[A-Za-z0-9._+-]+(/[A-Za-z0-9._+-]+)*$`)

// parseLatestQuery maps a URL, "gh:/gl:/cb:/sf:" prefix, owner/repo or bare name to a source.
// ponytail: only the public forges (github.com, gitlab.com, codeberg.org, sourceforge.net);
// self-hosted GitLab/Gitea would mean fetching arbitrary user-supplied hosts (SSRF guard needed).
func parseLatestQuery(q string) (latestQuery, error) {
	q = strings.TrimSpace(q)
	for p, k := range map[string]string{"gh:": "gh", "gl:": "gl", "cb:": "cb", "sf:": "sf"} {
		if strings.HasPrefix(strings.ToLower(q), p) {
			return latestCheck(k, q[len(p):])
		}
	}
	s := strings.TrimPrefix(strings.TrimPrefix(q, "https://"), "http://")
	s = strings.TrimPrefix(s, "www.")
	host, rest, _ := strings.Cut(s, "/")
	rest = strings.TrimSuffix(strings.TrimSuffix(rest, "/"), ".git")
	switch strings.ToLower(host) {
	case "github.com":
		return latestCheck("gh", firstSegments(rest, 2))
	case "gitlab.com":
		rest, _, _ = strings.Cut(rest, "/-/")
		return latestCheck("gl", rest)
	case "codeberg.org":
		return latestCheck("cb", firstSegments(rest, 2))
	case "sourceforge.net":
		return latestCheck("sf", strings.TrimPrefix(firstSegments(rest, 2), "projects/"))
	}
	if strings.Contains(q, "/") {
		return latestCheck("gh", q)
	}
	return latestCheck("repology", strings.ToLower(q))
}

func firstSegments(p string, n int) string {
	parts := strings.Split(p, "/")
	if len(parts) > n {
		parts = parts[:n]
	}
	return strings.Join(parts, "/")
}

func latestCheck(kind, name string) (latestQuery, error) {
	name = strings.Trim(name, "/")
	segs := strings.Count(name, "/") + 1
	ok := latestNameRe.MatchString(name) && len(name) <= 200 && !strings.Contains("/"+name+"/", "/./") && !strings.Contains("/"+name+"/", "/../")
	switch kind {
	case "gh", "cb":
		ok = ok && segs == 2
	case "gl":
		ok = ok && segs >= 2
	case "sf", "repology":
		ok = ok && segs == 1
	}
	if !ok {
		return latestQuery{}, fmt.Errorf("can't parse %q", name)
	}
	return latestQuery{kind, name}, nil
}

var latestPreRe = regexp.MustCompile(`(?i)(alpha|beta|rc\d*|pre|preview|dev|nightly|snapshot|canary)`)

func isPrerelease(v string) bool { return latestPreRe.MatchString(v) }

var digitsRe = regexp.MustCompile(`\d+`)

// compareVersions compares the numeric runs of two version strings ("v1.10.2" > "1.9").
// ponytail: numeric-only comparison, ignores suffixes; prereleases are filtered before this.
func compareVersions(a, b string) int {
	x, y := digitsRe.FindAllString(a, -1), digitsRe.FindAllString(b, -1)
	for i := 0; i < len(x) && i < len(y); i++ {
		n, _ := strconv.Atoi(x[i])
		m, _ := strconv.Atoi(y[i])
		if n != m {
			if n < m {
				return -1
			}
			return 1
		}
	}
	return len(x) - len(y)
}

// versionTagRe accepts "1.2", "v1.2.3", "go1.25", "release-1.2", "INKSCAPE_1_4"; not "weekly.2012-03-27".
var versionTagRe = regexp.MustCompile(`^[A-Za-z_-]*\d+([._]\d+)+`)

// highestTag picks the highest stable version-looking tag.
func highestTag(tags []string) string {
	best := ""
	for _, t := range tags {
		if isPrerelease(t) || !versionTagRe.MatchString(t) {
			continue
		}
		if best == "" || compareVersions(t, best) > 0 {
			best = t
		}
	}
	return best
}

var sfVersionRe = regexp.MustCompile(`\d+(?:[._]\d+)+[a-z]?`)

// sfVersion extracts a version from a SourceForge release path, preferring the file name.
func sfVersion(p string) string {
	if v := sfVersionRe.FindString(path.Base(p)); v != "" {
		return v
	}
	for _, seg := range strings.Split(p, "/") {
		if v := sfVersionRe.FindString(seg); v != "" {
			return v
		}
	}
	return ""
}

func latestGetJSON(ctx context.Context, rawURL, token string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", meta.Name+"/"+meta.Version+" (IRC bot)")
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := latestHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("service unavailable")
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return errLatestNotFound
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		return fmt.Errorf("rate limited, try later")
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 2<<20)).Decode(out); err != nil {
		return fmt.Errorf("bad response")
	}
	return nil
}

// latestResult is what gets printed.
type latestResult struct {
	version, title, date, link, note string
}

func fetchLatest(ctx context.Context, q latestQuery, ghToken string) (*latestResult, error) {
	switch q.kind {
	case "gh":
		return fetchLatestForge(ctx, latestGitHubAPI+"/repos/"+q.name, "https://github.com/"+q.name, ghToken, "per_page=100")
	case "cb":
		return fetchLatestForge(ctx, latestCodebergAPI+"/repos/"+q.name, "https://codeberg.org/"+q.name, "", "limit=50")
	case "gl":
		return fetchLatestGitLab(ctx, q.name)
	case "sf":
		return fetchLatestSF(ctx, q.name)
	default:
		return fetchLatestRepology(ctx, q.name)
	}
}

// fetchLatestForge handles GitHub and Gitea/Forgejo (same /releases/latest + /tags shape).
func fetchLatestForge(ctx context.Context, api, web, token, tagParams string) (*latestResult, error) {
	var rel struct {
		TagName     string `json:"tag_name"`
		Name        string `json:"name"`
		HTMLURL     string `json:"html_url"`
		PublishedAt string `json:"published_at"`
	}
	err := latestGetJSON(ctx, api+"/releases/latest", token, &rel)
	if err == nil && rel.TagName != "" {
		return &latestResult{version: rel.TagName, title: rel.Name, date: rel.PublishedAt, link: rel.HTMLURL}, nil
	}
	if err != nil && !errors.Is(err, errLatestNotFound) {
		return nil, err
	}
	// No releases (or repo missing): fall back to tags.
	// GitHub lists tags in reverse name order, so "weekly"/"release.*" can fill page 1.
	// ponytail: 3 pages cap; repos with 300+ junk tags sorting above the real ones miss.
	var names []string
	for page := 1; page <= 3; page++ {
		var tags []struct {
			Name string `json:"name"`
		}
		if err := latestGetJSON(ctx, fmt.Sprintf("%s/tags?%s&page=%d", api, tagParams, page), token, &tags); err != nil {
			if errors.Is(err, errLatestNotFound) {
				return nil, fmt.Errorf("repository not found")
			}
			return nil, err
		}
		for _, t := range tags {
			names = append(names, t.Name)
		}
		if len(tags) < 50 {
			break
		}
	}
	if best := highestTag(names); best != "" {
		return &latestResult{version: best, link: web + "/releases/tag/" + url.PathEscape(best), note: "tag"}, nil
	}
	return nil, fmt.Errorf("no releases or version tags")
}

func fetchLatestGitLab(ctx context.Context, name string) (*latestResult, error) {
	api := latestGitLabAPI + "/projects/" + url.PathEscape(name)
	var rels []struct {
		TagName    string `json:"tag_name"`
		Name       string `json:"name"`
		ReleasedAt string `json:"released_at"`
		Upcoming   bool   `json:"upcoming_release"`
		Links      struct {
			Self string `json:"self"`
		} `json:"_links"`
	}
	if err := latestGetJSON(ctx, api+"/releases?per_page=20", "", &rels); err != nil {
		if errors.Is(err, errLatestNotFound) {
			return nil, fmt.Errorf("project not found")
		}
		return nil, err
	}
	for _, r := range rels {
		if !r.Upcoming && !isPrerelease(r.TagName) {
			return &latestResult{version: r.TagName, title: r.Name, date: r.ReleasedAt, link: r.Links.Self}, nil
		}
	}
	var tags []struct {
		Name string `json:"name"`
	}
	if err := latestGetJSON(ctx, api+"/repository/tags?order_by=version&per_page=50", "", &tags); err != nil {
		return nil, err
	}
	names := make([]string, len(tags))
	for i, t := range tags {
		names[i] = t.Name
	}
	if best := highestTag(names); best != "" {
		return &latestResult{version: best, link: "https://gitlab.com/" + name + "/-/tags/" + url.PathEscape(best), note: "tag"}, nil
	}
	return nil, fmt.Errorf("no releases or version tags")
}

func fetchLatestSF(ctx context.Context, name string) (*latestResult, error) {
	var r struct {
		Release struct {
			Filename string `json:"filename"`
			Date     string `json:"date"`
			URL      string `json:"url"`
		} `json:"release"`
	}
	if err := latestGetJSON(ctx, latestSFBase+url.PathEscape(name)+"/best_release.json", "", &r); err != nil {
		if errors.Is(err, errLatestNotFound) {
			return nil, fmt.Errorf("project not found")
		}
		return nil, err
	}
	if r.Release.Filename == "" {
		return nil, fmt.Errorf("no files released")
	}
	v := sfVersion(r.Release.Filename)
	title := path.Base(r.Release.Filename)
	if v == "" {
		v, title = title, ""
	}
	return &latestResult{version: v, title: title, date: r.Release.Date,
		link: "https://sourceforge.net/projects/" + name + "/files/latest/download"}, nil
}

func fetchLatestRepology(ctx context.Context, name string) (*latestResult, error) {
	var pkgs []struct {
		Status  string `json:"status"`
		Version string `json:"version"`
	}
	if err := latestGetJSON(ctx, latestRepology+url.PathEscape(name), "", &pkgs); err != nil {
		return nil, err
	}
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("unknown project (try owner/repo or a forge URL)")
	}
	best := ""
	for _, p := range pkgs {
		if p.Status != "newest" {
			continue
		}
		if best == "" || compareVersions(p.Version, best) > 0 {
			best = p.Version
		}
	}
	if best == "" {
		return nil, fmt.Errorf("no stable version known")
	}
	return &latestResult{version: best, link: "https://repology.org/project/" + url.PathEscape(name) + "/versions"}, nil
}

func formatLatest(q latestQuery, r *latestResult, link string) string {
	src := map[string]string{"gh": "GitHub", "gl": "GitLab", "cb": "Codeberg", "sf": "SourceForge", "repology": "Repology"}[q.kind]
	parts := []string{"\x0303,01[LATEST]\x03 " + q.name + " " + ircBold + r.version + ircBold}
	if r.title != "" && r.title != r.version {
		parts[0] += " (" + r.title + ")"
	}
	if len(r.date) >= 10 {
		parts = append(parts, r.date[:10])
	}
	if r.note != "" {
		src += ", " + r.note
	}
	parts = append(parts, src)
	if link != "" {
		parts = append(parts, "🔗 "+link)
	}
	return strings.Join(parts, " · ")
}

// handleLatestCommand reports the newest released version of a project (!latest / !latestversion).
func (b *ircNetwork) handleLatestCommand(target, sender, query string) {
	q, err := parseLatestQuery(query)
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		var r *latestResult
		if r, err = fetchLatest(ctx, q, b.getCfg().GitHub.Token); err == nil {
			link := r.link
			if link != "" {
				link = rss.ShortenURLWithService(link, b.getCfg().RSS.URLShortener)
			}
			b.sendPrivmsg(target, b.sanitize(formatLatest(q, r, link)))
			return
		}
	}
	b.sendPrivmsg(target, b.sanitize(fmt.Sprintf("@%s: latest: %v", sender, err)))
}
