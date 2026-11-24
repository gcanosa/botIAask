package github

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// RepoMeta is display metadata attached to an announcement, sourced from cached config
// (not fetched per-poll — see format.go).
type RepoMeta struct {
	CachedDescription string
}

// Announcement is a fully-formatted, ready-to-broadcast IRC line for one GitHub event.
type Announcement struct {
	RepoFullName string
	Kind         string // "push" | "pull_request" | "release" | "issues" | "create" | "delete"
	// RefID is the natural GitHub identifier for this event (short SHA, "#N", or a
	// branch/tag name) — shown in the "[KIND RefID]" tag by format.go.
	RefID string
	// Link is the raw (un-shortened) URL embedded in Message, or "" for events with no
	// link (delete). Kept alongside Message so fetcher.go can swap in a shortened URL
	// right before broadcast without redoing all the extraction/formatting work above —
	// see shortenAnnouncementLink.
	Link    string
	Message string
	// ID is the dedup id inside the repo: the event id, or the commit SHA for a push line
	// (so a commit is announced once however it arrives: poll, webhook, or another branch).
	ID string
	// At orders lines chronologically within one cycle (commit or event time).
	At time.Time
}

// ExtractAnnouncement turns a raw Events API entry into an Announcement, or ok=false if
// the event type/action isn't one we announce (unknown type, a PR "synchronize", a
// release that isn't newly published, etc). Pushes are not handled here: see webhookPush
// and branchAnnouncements.
func ExtractAnnouncement(ev RawEvent, meta RepoMeta) (Announcement, bool) {
	switch ev.Type {
	case "PullRequestEvent":
		return extractPullRequest(ev, meta)
	case "ReleaseEvent":
		return extractRelease(ev, meta)
	case "IssuesEvent":
		return extractIssue(ev, meta)
	case "CreateEvent":
		return extractCreate(ev, meta)
	case "DeleteEvent":
		return extractDelete(ev, meta)
	default:
		return Announcement{}, false
	}
}

// commitAnnouncement is the one-line announcement of a single commit: title only (first
// line, clipped) and a link to the commit itself, so its preview shows that commit.
func commitAnnouncement(repo, who, branch, sha, message string, at time.Time) Announcement {
	short := sha
	if len(short) > 7 {
		short = short[:7]
	}
	link := "https://github.com/" + repo + "/commit/" + sha
	return Announcement{
		RepoFullName: repo,
		Kind:         "push",
		RefID:        short,
		Link:         link,
		ID:           sha,
		At:           at,
		Message:      formatPush(repo, who, branch, clip(firstLine(message), maxTitle), link, short),
	}
}

// branchAnnouncements lists what a branch gained since its previous head. commits is
// newest first (GET /commits?sha=head); the walk stops at old. If old is not among them
// (a force push, or more than len(commits) new commits) everything fetched counts as new.
func branchAnnouncements(repo, branch, old string, commits []Commit) []Announcement {
	var out []Announcement
	for _, c := range commits {
		if c.SHA == old {
			break
		}
		who := c.Commit.Author.Name
		if c.Author != nil && c.Author.Login != "" {
			who = c.Author.Login
		}
		out = append(out, commitAnnouncement(repo, who, branch, c.SHA, c.Commit.Message, c.Commit.Committer.Date))
	}
	// Oldest first. Commit dates are not monotonic along a chain (amend, rebase), so lift
	// each to its parent's: the time sort in announce() then never reorders a chain.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	for i := 1; i < len(out); i++ {
		if out[i].At.Before(out[i-1].At) {
			out[i].At = out[i-1].At
		}
	}
	return out
}

// webhookPush turns a push webhook delivery into one announcement per commit it carries
// (GitHub lists up to 20, oldest first). Events API pushes carry no commit list and are
// skipped: branch polling announces those in real time.
func webhookPush(ev RawEvent) []Announcement {
	var p struct {
		Ref     string `json:"ref"`
		Commits []struct {
			ID        string    `json:"id"`
			Message   string    `json:"message"`
			Timestamp time.Time `json:"timestamp"`
			Author    struct {
				Name     string `json:"name"`
				Username string `json:"username"`
			} `json:"author"`
		} `json:"commits"`
	}
	if json.Unmarshal(ev.Payload, &p) != nil {
		return nil
	}
	branch, ok := strings.CutPrefix(p.Ref, "refs/heads/")
	if !ok {
		return nil
	}
	var out []Announcement
	for _, c := range p.Commits {
		if c.ID == "" { // an Events API commit list (key "sha"): not a webhook
			continue
		}
		who := c.Author.Username
		if who == "" {
			who = c.Author.Name
		}
		if who == "" {
			who = ev.Actor.Login
		}
		at := c.Timestamp
		if at.IsZero() {
			at = ev.CreatedAt
		}
		out = append(out, commitAnnouncement(ev.Repo.Name, who, branch, c.ID, c.Message, at))
	}
	return out
}

type pullRequestPayload struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		HTMLURL string `json:"html_url"`
		Title   string `json:"title"`
		User    struct {
			Login string `json:"login"`
		} `json:"user"`
		Merged bool `json:"merged"`
		Head   struct {
			Ref string `json:"ref"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	} `json:"pull_request"`
}

func extractPullRequest(ev RawEvent, meta RepoMeta) (Announcement, bool) {
	var p pullRequestPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return Announcement{}, false
	}
	action := p.Action
	switch action {
	case "opened", "reopened", "merged":
		// announce as-is (GitHub's trimmed Events API reports a merge as action "merged"
		// directly, rather than "closed" + a separate merged:true field)
	case "closed":
		if p.PullRequest.Merged {
			action = "merged"
		} else {
			return Announcement{}, false
		}
	default:
		return Announcement{}, false
	}

	// The trimmed payload also drops pull_request.user/title/html_url. The actor at the
	// top level of the event is always populated, so prefer it; fall back to head/base
	// branch names for a title when none was given.
	author := ev.Actor.Login
	if p.PullRequest.User.Login != "" {
		author = p.PullRequest.User.Login
	}
	title := p.PullRequest.Title
	if title == "" && p.PullRequest.Head.Ref != "" {
		title = p.PullRequest.Head.Ref + " -> " + p.PullRequest.Base.Ref
	}
	link := p.PullRequest.HTMLURL
	if link == "" {
		link = "https://github.com/" + ev.Repo.Name + "/pull/" + strconv.Itoa(p.Number)
	}
	refID := "#" + strconv.Itoa(p.Number)
	return Announcement{
		RepoFullName: ev.Repo.Name,
		Kind:         "pull_request",
		RefID:        refID,
		Link:         link,
		Message:      formatPullRequest(ev.Repo.Name, author, action, refID, title, link),
	}, true
}

type releasePayload struct {
	Action  string `json:"action"`
	Release struct {
		TagName string `json:"tag_name"`
		Name    string `json:"name"`
		HTMLURL string `json:"html_url"`
		Author  struct {
			Login string `json:"login"`
		} `json:"author"`
	} `json:"release"`
}

func extractRelease(ev RawEvent, meta RepoMeta) (Announcement, bool) {
	var p releasePayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return Announcement{}, false
	}
	if p.Action != "published" {
		return Announcement{}, false
	}
	author := p.Release.Author.Login
	if author == "" {
		author = ev.Actor.Login
	}
	link := p.Release.HTMLURL
	if link == "" {
		link = "https://github.com/" + ev.Repo.Name + "/releases/tag/" + p.Release.TagName
	}
	return Announcement{
		RepoFullName: ev.Repo.Name,
		Kind:         "release",
		RefID:        p.Release.TagName,
		Link:         link,
		Message:      formatRelease(ev.Repo.Name, author, p.Release.TagName, p.Release.Name, link),
	}, true
}

type issuesPayload struct {
	Action string `json:"action"`
	Issue  struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		HTMLURL string `json:"html_url"`
		User    struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"issue"`
}

// extractIssue only announces newly opened or closed issues — labeled/assigned/edited/
// reopened and other lifecycle actions are intentionally not announced.
func extractIssue(ev RawEvent, meta RepoMeta) (Announcement, bool) {
	var p issuesPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return Announcement{}, false
	}
	switch p.Action {
	case "opened", "closed":
	default:
		return Announcement{}, false
	}

	author := ev.Actor.Login
	if p.Issue.User.Login != "" {
		author = p.Issue.User.Login
	}
	link := p.Issue.HTMLURL
	if link == "" {
		link = "https://github.com/" + ev.Repo.Name + "/issues/" + strconv.Itoa(p.Issue.Number)
	}
	refID := "#" + strconv.Itoa(p.Issue.Number)
	return Announcement{
		RepoFullName: ev.Repo.Name,
		Kind:         "issues",
		RefID:        refID,
		Link:         link,
		Message:      formatIssue(ev.Repo.Name, author, p.Action, refID, p.Issue.Title, link),
	}, true
}

type createDeletePayload struct {
	RefType string `json:"ref_type"` // "branch" | "tag" | "repository"
	Ref     string `json:"ref"`
}

// extractCreate announces new branches/tags. ref_type "repository" (CreateEvent also
// fires when the repo itself is created) is ignored — not relevant to activity tracking.
func extractCreate(ev RawEvent, meta RepoMeta) (Announcement, bool) {
	var p createDeletePayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil || p.Ref == "" {
		return Announcement{}, false
	}
	if p.RefType != "branch" && p.RefType != "tag" {
		return Announcement{}, false
	}
	link := "https://github.com/" + ev.Repo.Name + "/tree/" + p.Ref
	return Announcement{
		RepoFullName: ev.Repo.Name,
		Kind:         "create",
		RefID:        p.Ref,
		Link:         link,
		Message:      formatCreate(ev.Repo.Name, ev.Actor.Login, p.RefType, p.Ref, link),
	}, true
}

// extractDelete announces branch/tag deletion. There is deliberately no link: the ref no
// longer exists once this event fires.
func extractDelete(ev RawEvent, meta RepoMeta) (Announcement, bool) {
	var p createDeletePayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil || p.Ref == "" {
		return Announcement{}, false
	}
	if p.RefType != "branch" && p.RefType != "tag" {
		return Announcement{}, false
	}
	return Announcement{
		RepoFullName: ev.Repo.Name,
		Kind:         "delete",
		RefID:        p.Ref,
		Message:      formatDelete(ev.Repo.Name, ev.Actor.Login, p.RefType, p.Ref),
	}, true
}

// maxTitle bounds the title part of a line: a pasted essay of a commit message would
// otherwise wrap over many IRC lines.
const maxTitle = 150

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
