package ircusage

import (
	"sort"
	"strings"
)

// Row is one usage line of a command, e.g. Cmd "!gh list", Desc "List tracked GitHub repos".
type Row struct {
	Cmd, Desc string
	Admin     bool
}

// about holds an optional one-line explanation shown first by "!help <cmd>" for commands
// whose rows alone don't say what the feature is.
var about = map[string]string{
	"gh":     "Tracks GitHub repos and announces pushes, PRs, releases, issues and branch/tag events in IRC channels.",
	"ticket": "Approve or cancel pending upload tickets (files/pastes waiting for staff approval).",
}

func init() {
	// Keep every listing (CLI reference and IRC !help) alphabetical.
	for _, ls := range [][]line{userLines, adminLines} {
		sort.SliceStable(ls, func(i, j int) bool { return ls[i].cmd < ls[j].cmd })
	}
}

func cmdName(cmd string) string {
	f := strings.Fields(strings.TrimPrefix(cmd, "!"))
	if len(f) == 0 {
		return ""
	}
	return strings.ToLower(f[0])
}

// Names returns the sorted, de-duplicated command names (no prefix) for user or admin commands.
func Names(admin bool) []string {
	ls := userLines
	if admin {
		ls = adminLines
	}
	var out []string
	for _, l := range ls {
		n := cmdName(l.cmd)
		if len(out) == 0 || out[len(out)-1] != n {
			out = append(out, n)
		}
	}
	return out
}

// Lookup returns every usage row for the named command (user rows first) and its optional
// explanation. ok is false when the command is unknown.
func Lookup(name string) (rows []Row, intro string, ok bool) {
	name = strings.ToLower(strings.TrimPrefix(name, "!"))
	for _, set := range []struct {
		ls    []line
		admin bool
	}{{userLines, false}, {adminLines, true}} {
		for _, l := range set.ls {
			if cmdName(l.cmd) == name {
				rows = append(rows, Row{l.cmd, l.desc, set.admin})
			}
		}
	}
	return rows, about[name], len(rows) > 0
}
