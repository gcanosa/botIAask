package github

import (
	"fmt"
	"strconv"
)

// IRC mIRC color codes: \x03FG,BG text \x03, bold \x02 — same escape family as rss/irc_source.go.
const (
	ircPushTag    = "\x0309,01[PUSH]\x03"    // green on black
	ircPRTag      = "\x0312,01[PR]\x03"      // blue on black
	ircReleaseTag = "\x0306,01[RELEASE]\x03" // purple on black
	ircBold       = "\x02"
	ircLinkSuffix = " \x0312\x1f🔗\x1f\x03 %s" // light blue + underlined, then URL
)

func formatPush(repoFullName, pusher, branch string, size int, headline, more, link string) string {
	base := ircPushTag + " " + ircBold + repoFullName + ircBold + " " + pusher
	if headline == "" {
		// GitHub's Events API didn't include a commit list for this push, so there's no
		// message/count to show — just the fact that something landed, and a link to it.
		return base + " pushed to " + ircBold + branch + ircBold + sprintfLink(link)
	}
	return base + " pushed " + strconv.Itoa(size) + " commit" + plural(size) + " to " + ircBold + branch + ircBold +
		`: "` + headline + `"` + more + sprintfLink(link)
}

func formatPullRequest(repoFullName, author, action string, number int, title, link string) string {
	return ircPRTag + " " + ircBold + repoFullName + ircBold + " " + author +
		" " + action + " PR #" + strconv.Itoa(number) + `: "` + title + `"` + sprintfLink(link)
}

func formatRelease(repoFullName, author, tag, name, link string) string {
	return ircReleaseTag + " " + ircBold + repoFullName + ircBold + " " + author +
		" published " + tag + ` "` + name + `"` + sprintfLink(link)
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
