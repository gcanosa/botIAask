package rss

import (
	"fmt"
	"strings"
	"unicode"
)

// IRC mIRC color codes: \x03FG,BG text \x03
const (
	ircNewsRedOnBlack  = "\x0304,01[NEWS]\x03" // [NEWS] (unchanged)
	ircSourceHN        = "\x0307,01[HN]\x03"  // Hacker News: orange (7) on black (1)
	ircSourceFallback  = "\x0314,01"           // grey (14) on black (1), closed per tag
	ircLinkEmojiSuffix = " \x0312\x1f🔗\x1f\x03 %s" // light blue + underlined, then URL
)

// IRCSourceTagMIRC returns a colored [TAG] mIRC segment for a feed source key, or "" if key is empty.
// Known keys get curated colors; unknown keys get a 2–3 letter fallback label in grey on black.
func IRCSourceTagMIRC(sourceKey string) string {
	if strings.TrimSpace(sourceKey) == "" {
		return ""
	}
	switch sourceKey {
	case "hacker-news":
		return ircSourceHN
	default:
		label := ircSourceLabelFallback(sourceKey)
		if label == "" {
			label = "?"
		}
		return ircSourceFallback + "[" + label + "]\x03"
	}
}

func ircSourceLabelFallback(key string) string {
	var b strings.Builder
	for _, r := range key {
		if r == '-' || r == '_' {
			continue
		}
		if !unicode.IsLetter(r) && !unicode.IsNumber(r) {
			continue
		}
		b.WriteRune(unicode.ToUpper(r))
		if b.Len() >= 3 {
			break
		}
	}
	return b.String()
}

// FormatIRCNewsLine builds: [NEWS][SOURCE?] 15:04 - Title [🔗 link] (mIRC)
// link should be the shortened URL to show, or empty to omit the link part.
func FormatIRCNewsLine(e NewsEntry, link string) string {
	msg := ircNewsRedOnBlack + IRCSourceTagMIRC(e.Source) + " " + e.PubDate.Format("15:04") + " - " + e.Title
	if link != "" {
		msg += fmt.Sprintf(ircLinkEmojiSuffix, link)
	}
	return msg
}
