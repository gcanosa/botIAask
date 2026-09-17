package github

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
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
}

// ExtractAnnouncement turns a raw Events API entry into an Announcement, or ok=false if
// the event type/action isn't one we announce (unknown type, a PR "synchronize", a
// release that isn't newly published, etc).
func ExtractAnnouncement(ev RawEvent, meta RepoMeta) (Announcement, bool) {
	switch ev.Type {
	case "PushEvent":
		return extractPush(ev, meta)
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

type pushPayload struct {
	Ref     string `json:"ref"`
	Size    int    `json:"size"`
	Head    string `json:"head"`
	Before  string `json:"before"`
	Commits []struct {
		SHA     string `json:"sha"`
		Message string `json:"message"`
	} `json:"commits"`
}

func extractPush(ev RawEvent, meta RepoMeta) (Announcement, bool) {
	var p pushPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil || p.Head == "" {
		return Announcement{}, false
	}
	branch := strings.TrimPrefix(p.Ref, "refs/heads/")

	// GitHub's public Events API stopped populating "commits"/"size" on PushEvent payloads
	// (confirmed empirically against github.com in 2026-09; still checked here in case a
	// GitHub Enterprise instance or a future API version restores it). Without it we can
	// only report that a push happened, not what was in it.
	var headline, more string
	if len(p.Commits) > 0 {
		headline = firstLine(p.Commits[len(p.Commits)-1].Message)
		if p.Size > 1 {
			more = " (+" + strconv.Itoa(p.Size-1) + " more)"
		}
	}

	link := "https://github.com/" + ev.Repo.Name + "/commit/" + p.Head
	if p.Before != "" && p.Before != p.Head && p.Size != 1 {
		link = "https://github.com/" + ev.Repo.Name + "/compare/" + p.Before + "..." + p.Head
	}
	shortSHA := p.Head
	if len(shortSHA) > 7 {
		shortSHA = shortSHA[:7]
	}
	return Announcement{
		RepoFullName: ev.Repo.Name,
		Kind:         "push",
		RefID:        shortSHA,
		Link:         link,
		Message:      formatPush(ev.Repo.Name, ev.Actor.Login, branch, p.Size, headline, more, link, shortSHA),
	}, true
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

// CollapseForBroadcast reduces the announcements gathered from one poll cycle to at most
// one line per event kind: the most recent item of that kind, noting how many more were
// folded in. Without this, a burst of activity (several pushes during one dev session, or
// the one-time catch-up when a repo is first tracked) would flood the channel with one
// line per raw event. anns must be ordered oldest-to-newest.
func CollapseForBroadcast(anns []Announcement) []Announcement {
	type group struct {
		latest Announcement
		count  int
	}
	var order []string
	groups := make(map[string]*group, 3)
	for _, ann := range anns {
		g, ok := groups[ann.Kind]
		if !ok {
			g = &group{}
			groups[ann.Kind] = g
			order = append(order, ann.Kind)
		}
		g.latest = ann // oldest-to-newest input means the last write is the latest
		g.count++
	}

	out := make([]Announcement, 0, len(order))
	for _, kind := range order {
		g := groups[kind]
		ann := g.latest
		if g.count > 1 {
			ann.Message += fmt.Sprintf(" (+%d more %s)", g.count-1, kindLabel(kind))
		}
		out = append(out, ann)
	}
	return out
}

func kindLabel(kind string) string {
	switch kind {
	case "push":
		return "pushes"
	case "pull_request":
		return "PR updates"
	case "release":
		return "releases"
	case "issues":
		return "issues"
	case "create":
		return "refs created"
	case "delete":
		return "refs deleted"
	default:
		return kind
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
