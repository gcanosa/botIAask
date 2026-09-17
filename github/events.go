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
	Kind         string // "push" | "pull_request" | "release"
	Message      string
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
	return Announcement{
		RepoFullName: ev.Repo.Name,
		Kind:         "push",
		Message:      formatPush(ev.Repo.Name, ev.Actor.Login, branch, p.Size, headline, more, link),
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
	return Announcement{
		RepoFullName: ev.Repo.Name,
		Kind:         "pull_request",
		Message:      formatPullRequest(ev.Repo.Name, author, action, p.Number, title, link),
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
	name := p.Release.Name
	if name == "" {
		name = p.Release.TagName
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
		Message:      formatRelease(ev.Repo.Name, author, p.Release.TagName, name, link),
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
