package logger

import "strings"

// ChannelFileKey returns the filesystem-safe channel segment used in log filenames
// ({key}_{YYYY-MM-DD}.log). It must match the logic in LogChannelEvent.
func ChannelFileKey(channel, serverName string) string {
	safe := strings.ReplaceAll(channel, "/", "_")
	if len(safe) > 0 && (safe[0] == '#' || safe[0] == '&') {
		safe = safe[1:]
	} else if len(safe) == 0 || (safe[0] != '#' && safe[0] != '&') {
		safe = serverName
	}
	return safe
}
