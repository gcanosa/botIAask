package github

import (
	"fmt"
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

// Per-field mIRC colors so author/SHA/branch stand out when scanning a busy channel.
func colorize(code, s string) string { return "\x03" + code + s + "\x03" }

func author(s string) string { return colorize("04", s) } // light red / pink
func sha(s string) string    { return colorize("13", s) } // magenta
func ref(s string) string    { return colorize("07", s) } // orange

// prefix renders "<tag> (owner/repo) author", the common start of every announcement.
func prefix(tag, repoFullName, who string) string {
	return tag + " " + colorize("14", "("+repoFullName+")") + " " + author(who)
}

func formatPush(repoFullName, pusher, branch string, size int, headline, more, link, shortSHA string) string {
	base := prefix(ircTag("09", "PUSH", ""), repoFullName, pusher) + " pushed " + sha(shortSHA) + " to " + ref(branch)
	if headline == "" {
		// GitHub's Events API didn't include a commit list for this push, so there's no
		// message to show — just the fact that something landed, and a link to it.
		return base + sprintfLink(link)
	}
	return base + ": " + headline + more + sprintfLink(link)
}

func formatPullRequest(repoFullName, who, action, refID, title, link string) string {
	return prefix(ircTag("12", "PR", refID), repoFullName, who) + " " + action + ": " + title + sprintfLink(link)
}

func formatRelease(repoFullName, who, tag, name, link string) string {
	base := prefix(ircTag("06", "RELEASE", tag), repoFullName, who) + " published"
	if name != "" && name != tag {
		base += ` "` + name + `"`
	}
	return base + sprintfLink(link)
}

func formatIssue(repoFullName, who, action, refID, title, link string) string {
	return prefix(ircTag("08", "ISSUE", refID), repoFullName, who) + " " + action + " issue: " + title + sprintfLink(link)
}

func formatCreate(repoFullName, who, refType, refID, link string) string {
	return prefix(ircTag("10", strings.ToUpper(refType), refID), repoFullName, who) + " created " + refType + sprintfLink(link)
}

// formatDelete has no link parameter: the ref no longer exists once a DeleteEvent fires.
func formatDelete(repoFullName, who, refType, refID string) string {
	return prefix(ircTag("04", strings.ToUpper(refType), refID), repoFullName, who) + " deleted " + refType
}

func sprintfLink(link string) string {
	if link == "" {
		return ""
	}
	return fmt.Sprintf(ircLinkSuffix, link)
}
