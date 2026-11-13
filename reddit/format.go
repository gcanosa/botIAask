package reddit

import (
	"html"
	"strings"
)

const (
	ircRedditTag = "\x0300,04[REDDIT]\x03" // white on red
	ircSubColor  = "\x0307"                // orange
	ircLinkFmt   = " \x0312\x1f🔗\x1f\x03 " // same link style as rss.FormatIRCNewsLine
	maxTitle     = 300
)

// FormatLine: [REDDIT] r/golang 15:04 - Title (u/author) 🔗 https://redd.it/abc
func FormatLine(p Post) string {
	title := clean(p.Title)
	if r := []rune(title); len(r) > maxTitle {
		title = string(r[:maxTitle]) + "…"
	}
	msg := ircRedditTag + " " + ircSubColor + "r/" + clean(p.Sub) + "\x03 " + p.Published.Local().Format("15:04") + " - " + title
	if a := clean(p.Author); a != "" {
		msg += " \x0314(u/" + a + ")\x03"
	}
	return msg + ircLinkFmt + p.Link
}

// clean unescapes HTML entities and drops control characters, so a post title can't carry
// IRC formatting codes or line breaks into the channel.
func clean(s string) string {
	s = html.UnescapeString(s)
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)), " ")
}
