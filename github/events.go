package github

import (
	"encoding/json"
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
	if err := json.Unmarshal(ev.Payload, &p); err != nil || len(p.Commits) == 0 {
		return Announcement{}, false
	}
	branch := strings.TrimPrefix(p.Ref, "refs/heads/")
	headline := firstLine(p.Commits[len(p.Commits)-1].Message)
	more := ""
	if p.Size > 1 {
		more = " (+" + strconv.Itoa(p.Size-1) + " more)"
	}
	link := "https://github.com/" + ev.Repo.Name + "/commit/" + p.Head
	if p.Size > 1 && p.Before != "" {
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
	} `json:"pull_request"`
}

func extractPullRequest(ev RawEvent, meta RepoMeta) (Announcement, bool) {
	var p pullRequestPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return Announcement{}, false
	}
	action := p.Action
	switch action {
	case "opened", "reopened":
		// announce as-is
	case "closed":
		if p.PullRequest.Merged {
			action = "merged"
		}
	default:
		return Announcement{}, false
	}
	return Announcement{
		RepoFullName: ev.Repo.Name,
		Kind:         "pull_request",
		Message:      formatPullRequest(ev.Repo.Name, p.PullRequest.User.Login, action, p.Number, p.PullRequest.Title, p.PullRequest.HTMLURL),
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
	return Announcement{
		RepoFullName: ev.Repo.Name,
		Kind:         "release",
		Message:      formatRelease(ev.Repo.Name, p.Release.Author.Login, p.Release.TagName, name, p.Release.HTMLURL),
	}, true
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
