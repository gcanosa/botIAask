package github

import (
	"fmt"
	"strconv"
	"strings"

	"botIAask/rss"
)

// IRC mIRC color codes: \x03FG,BG text \x03, bold \x02 — same escape family as rss/irc_source.go.
const (
	ircBold       = "\x02"
	ircLinkSuffix = " \x0312\x1f🔗\x1f\x03 %s" // light blue + underlined, then URL
)

// ircTag builds a "[LABEL]" or "[LABEL refID]" bracketed tag in the given mIRC foreground
// color on a black background. refID is the natural GitHub identifier for the event (a
// short SHA, "#N", or a branch/tag name) — putting it in the tag itself keeps it visible
// even if the message gets truncated, rather than buried in body text.
func ircTag(colorCode, label, refID string) string {
	if refID == "" {
		return "\x03" + colorCode + ",01[" + label + "]\x03"
	}
	return "\x03" + colorCode + ",01[" + label + " " + refID + "]\x03"
}

// shortenURLFunc is rss.ShortenURLWithService by default; tests override it to avoid real
// network calls (same pattern as apiBase/eventsHTTPClient in client.go).
var shortenURLFunc = rss.ShortenURLWithService

// shortenAnnouncementLink swaps ann.Link (if any) for a shortened URL inside ann.Message
// via the bot's existing rss.ShortenURLWithService (the same shortener already used for
// RSS article links) — freeing up message space for description text instead of a long
// github.com URL. Falls back to the original link if shortening fails (that's
// ShortenURLWithService's own behavior) or if there's no link at all (delete events).
func shortenAnnouncementLink(ann Announcement, preferredService string) string {
	if ann.Link == "" {
		return ann.Message
	}
	short := shortenURLFunc(ann.Link, preferredService)
	if short == "" || short == ann.Link {
		return ann.Message
	}
	return strings.Replace(ann.Message, ann.Link, short, 1)
}

func formatPush(repoFullName, pusher, branch string, size int, headline, more, link, shortSHA string) string {
	tag := ircTag("09", "PUSH", shortSHA) // green on black
	base := tag + " " + ircBold + repoFullName + ircBold + " " + pusher
	if headline == "" {
		// GitHub's Events API didn't include a commit list for this push, so there's no
		// message/count to show — just the fact that something landed, and a link to it.
		return base + " pushed to " + ircBold + branch + ircBold + sprintfLink(link)
	}
	return base + " pushed " + strconv.Itoa(size) + " commit" + plural(size) + " to " + ircBold + branch + ircBold +
		`: "` + headline + `"` + more + sprintfLink(link)
}

func formatPullRequest(repoFullName, author, action, refID, title, link string) string {
	tag := ircTag("12", "PR", refID) // blue on black
	return tag + " " + ircBold + repoFullName + ircBold + " " + author +
		" " + action + `: "` + title + `"` + sprintfLink(link)
}

func formatRelease(repoFullName, author, tag, name, link string) string {
	ircTagStr := ircTag("06", "RELEASE", tag) // purple on black
	base := ircTagStr + " " + ircBold + repoFullName + ircBold + " " + author + " published"
	if name != "" && name != tag {
		base += ` "` + name + `"`
	}
	return base + sprintfLink(link)
}

func formatIssue(repoFullName, author, action, refID, title, link string) string {
	tag := ircTag("08", "ISSUE", refID) // yellow on black
	return tag + " " + ircBold + repoFullName + ircBold + " " + author +
		" " + action + ` issue: "` + title + `"` + sprintfLink(link)
}

func formatCreate(repoFullName, author, refType, refID, link string) string {
	tag := ircTag("10", strings.ToUpper(refType), refID) // teal on black
	return tag + " " + ircBold + repoFullName + ircBold + " " + author +
		" created " + refType + sprintfLink(link)
}

// formatDelete has no link parameter: the ref no longer exists once a DeleteEvent fires.
func formatDelete(repoFullName, author, refType, refID string) string {
	tag := ircTag("04", strings.ToUpper(refType), refID) // red on black
	return tag + " " + ircBold + repoFullName + ircBold + " " + author + " deleted " + refType
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func sprintfLink(link string) string {
	if link == "" {
		return ""
	}
	return fmt.Sprintf(ircLinkSuffix, link)
}
