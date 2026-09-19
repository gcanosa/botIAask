package irc

import (
	"fmt"
	"strconv"
	"strings"
)

// Helpers for the admin !stats layout. mIRC colors on black, same convention as weather_cmd.go.
const (
	statsCyan   = "11"
	statsWhite  = "00"
	statsGreen  = "09"
	statsYellow = "08"
	statsRed    = "04"
	statsGray   = "14"
)

func statsColor(col, s string) string { return ircCol + col + ",01" + s + ircEnd }

// statsChip is the bold colored section tag that starts each line, e.g. "[BOT]".
func statsChip(col, name string) string {
	return ircBold + statsColor(col, "["+name+"]") + ircBold
}

// statsKV renders "key value" with a dim key and a bright value.
func statsKV(k, v string) string {
	return statsColor(statsGray, k) + " " + statsColor(statsWhite, v)
}

// statsJoin joins parts with a gray middle dot.
func statsJoin(parts ...string) string {
	return strings.Join(parts, " "+statsColor(statsGray, "·")+" ")
}

func statsOnOffC(v bool) string {
	if v {
		return statsColor(statsGreen, "on")
	}
	return statsColor(statsRed, "off")
}

// statsPctColor: green <60, yellow <85, red otherwise.
func statsPctColor(p float64) string {
	switch {
	case p < 60:
		return statsGreen
	case p < 85:
		return statsYellow
	}
	return statsRed
}

// statsNum formats n with thousands separators; negatives (unavailable) render as gray "n/a".
func statsNum(n int) string {
	if n < 0 {
		return statsColor(statsGray, "n/a")
	}
	return statsColor(statsWhite, statsGroup(n))
}

func statsGroup(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func statsPct(label string, p float64) string {
	return statsColor(statsGray, label) + " " + statsColor(statsPctColor(p), fmt.Sprintf("%.0f%%", p))
}
