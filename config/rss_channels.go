package config

import "strings"

// RSSChannelContainsFold reports whether list has a channel matching name (case-insensitive, trimmed).
func RSSChannelContainsFold(list []string, name string) bool {
	n := strings.TrimSpace(name)
	if n == "" {
		return false
	}
	for _, c := range list {
		if strings.EqualFold(strings.TrimSpace(c), n) {
			return true
		}
	}
	return false
}

// SetRSSChannelAnnounce adds or removes a channel in the RSS broadcast list. Matching is
// case-insensitive. When on is true, canonical is appended if no folded match exists; if canonical is empty, name (trimmed) is used.
func SetRSSChannelAnnounce(list []string, name string, on bool, canonical string) []string {
	n := strings.TrimSpace(name)
	if n == "" {
		return list
	}
	if on {
		canon := strings.TrimSpace(canonical)
		if canon == "" {
			canon = n
		}
		for _, c := range list {
			if strings.EqualFold(strings.TrimSpace(c), n) {
				return list
			}
		}
		return append(append([]string(nil), list...), canon)
	}
	var out []string
	for _, c := range list {
		if !strings.EqualFold(strings.TrimSpace(c), n) {
			out = append(out, c)
		}
	}
	return out
}
