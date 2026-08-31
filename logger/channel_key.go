package logger

import "strings"

// ChannelFileKey returns the filesystem-safe channel segment used in log filenames
// ({key}_{YYYY-MM-DD}.log). It must match the logic in LogChannelEvent.
//
// Channel-type names are prefixed with serverName ("<network>_<channel>") so two
// networks sharing a channel name (e.g. both having #general) log to distinct files
// instead of interleaving in one — see CLAUDE.md's documented logs/<server>_<channel>
// format.
func ChannelFileKey(channel, serverName string) string {
	safe := strings.ReplaceAll(channel, "/", "_")
	if len(safe) > 0 && (safe[0] == '#' || safe[0] == '&') {
		safe = safe[1:]
		if serverName != "" {
			safe = serverName + "_" + safe
		}
	} else if len(safe) == 0 || (safe[0] != '#' && safe[0] != '&') {
		safe = serverName
	}
	return safe
}
