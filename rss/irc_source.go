package rss

import (
	"fmt"
	"strings"
	"unicode"
)

// IRC mIRC color codes: \x03FG,BG text \x03
const (
	ircNewsRedOnBlack  = "\x0304,01[NEWS]\x03"     // [NEWS] (unchanged)
	ircSourceHN        = "\x0307,01[HN]\x03"       // Hacker News: orange (7) on black (1)
	ircSourceFallback  = "\x0314,01"               // grey (14) on black (1), closed per tag
	ircLinkEmojiSuffix = " \x0312\x1f🔗\x1f\x03 %s" // light blue + underlined, then URL
)

// IRCSourceTagMIRC returns a colored [TAG] mIRC segment for a feed source key, or "" if key is empty.
// Known keys get curated colors; unknown keys get a full-word uppercase label from the key (e.g. apple → [APPLE], lobsters-mirror → [LOBSTERS-MIRROR]).
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
	key = strings.TrimSpace(strings.ToLower(key))
	if key == "" {
		return ""
	}
	segments := strings.FieldsFunc(key, func(r rune) bool { return r == '-' || r == '_' })
	if len(segments) == 0 {
		return ""
	}
	var b strings.Builder
	for _, seg := range segments {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		var part strings.Builder
		for _, r := range seg {
			if unicode.IsLetter(r) || unicode.IsNumber(r) {
				part.WriteRune(unicode.ToUpper(r))
			}
		}
		if part.Len() == 0 {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('-')
		}
		b.WriteString(part.String())
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
