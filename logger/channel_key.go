package logger

import (
	"os"
	"path/filepath"
	"strings"
)

// ChannelFileKey returns the filesystem-safe channel segment used in log filenames
// ({key}_{YYYY-MM-DD}.log). It must match the logic in LogChannelEvent.
//
// Channel-type names are prefixed with serverName ("<network>_<channel>") so two
// networks sharing a channel name (e.g. both having #general) log to distinct files
// instead of interleaving in one — see CLAUDE.md's documented logs/<server>_<channel>
// format.
//
// The channel part is lower-cased: IRC channel names are case-insensitive and the same
// channel arrives spelled differently (config "#News" for the bot's own lines, "#news" as
// relayed by the server), which used to split one day across several files on
// case-sensitive filesystems. Logs written before that still exist; see DayLogPaths.
func ChannelFileKey(channel, serverName string) string {
	safe := strings.ReplaceAll(channel, "/", "_")
	if len(safe) > 0 && (safe[0] == '#' || safe[0] == '&') {
		safe = strings.ToLower(safe[1:])
		if serverName != "" {
			safe = serverName + "_" + safe
		}
	} else if len(safe) == 0 || (safe[0] != '#' && safe[0] != '&') {
		safe = serverName
	}
	return safe
}

// DayLogPaths returns every file in dir named <key>_<date><ext> ignoring case (ext is ".log" or
// ".log.gz"), so a day logged before keys were lower-cased under several spellings of the same
// channel is read in full. The canonical (exact) name comes first when it exists; a name that
// resolves to an already-listed file (case-insensitive filesystems) is skipped.
func DayLogPaths(dir, key, date, ext string) []string {
	want := key + "_" + date + ext
	var out []string
	var seen []os.FileInfo
	add := func(p string) {
		fi, err := os.Stat(p)
		if err != nil || fi.IsDir() {
			return
		}
		for _, s := range seen {
			if os.SameFile(s, fi) {
				return
			}
		}
		seen = append(seen, fi)
		out = append(out, p)
	}
	add(filepath.Join(dir, want))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if strings.EqualFold(e.Name(), want) {
			add(filepath.Join(dir, e.Name()))
		}
	}
	return out
}
