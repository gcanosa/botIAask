package irc

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"botIAask/logger"
)

const chanStatsMaxDays = 30

// chanStatsBar renders 24 colored blocks (00h..23h), scaled to the busiest hour.
func chanStatsBar(h [24]int) string {
	max := 0
	for _, v := range h {
		if v > max {
			max = v
		}
	}
	blocks := []rune("▁▂▃▄▅▆▇█")
	var sb strings.Builder
	for _, v := range h {
		if v == 0 || max == 0 {
			sb.WriteString(statsColor(statsGray, "▁"))
			continue
		}
		r := float64(v) / float64(max)
		col := "12" // light blue
		switch {
		case r > 0.8:
			col = statsRed
		case r > 0.6:
			col = "07" // orange
		case r > 0.4:
			col = statsYellow
		case r > 0.2:
			col = statsGreen
		}
		i := int(r * 7.999)
		sb.WriteString(statsColor(col, string(blocks[i])))
	}
	return sb.String()
}

// chanStatsLines formats the reply lines (without the "@nick: " mention).
func chanStatsLines(channel string, days int, a logger.Activity) []string {
	head := statsChip(statsCyan, "CHANSTATS") + " " + statsJoin(
		statsColor(statsWhite, channel),
		statsKV("last", fmt.Sprintf("%dd", days)),
		statsKV("msgs", statsGroup(a.Total)),
		statsKV("users", statsGroup(len(a.Nicks))),
		statsKV("avg", statsGroup(a.Total/max(a.Days, 1))+"/day"),
	)
	if a.Total == 0 {
		return []string{head, statsColor(statsGray, "no activity found in the logs")}
	}
	bar := statsColor(statsGray, "00h ") + chanStatsBar(a.Hours) + statsColor(statsGray, " 24h (bot time)")

	bi, qi := 0, 0
	for h, v := range a.Hours {
		if v > a.Hours[bi] {
			bi = h
		}
		if v < a.Hours[qi] {
			qi = h
		}
	}
	di := 0
	for d, v := range a.Weekday {
		if v > a.Weekday[di] {
			di = d
		}
	}
	summary := statsJoin(
		statsKV("busiest", fmt.Sprintf("%02d:00-%02d:00 (%.1f%%)", bi, (bi+1)%24, 100*float64(a.Hours[bi])/float64(a.Total))),
		statsKV("quietest", fmt.Sprintf("%02d:00", qi)),
		statsKV("peak day", fmt.Sprintf("%s (%s)", time.Weekday(di).String()[:3], statsGroup(a.Weekday[di]))),
	)

	type nc struct {
		n string
		c int
	}
	top := make([]nc, 0, len(a.Nicks))
	for n, c := range a.Nicks {
		top = append(top, nc{n, c})
	}
	sort.Slice(top, func(i, j int) bool {
		if top[i].c != top[j].c {
			return top[i].c > top[j].c
		}
		return top[i].n < top[j].n
	})
	if len(top) > 5 {
		top = top[:5]
	}
	parts := make([]string, len(top))
	for i, t := range top {
		parts[i] = statsKV(t.n, statsGroup(t.c))
	}
	return []string{head, bar, summary, statsColor(statsGray, "top: ") + statsJoin(parts...)}
}

// handleChanStatsCommand: !chanstats [#chan] [days] — public, built from the channel's daily logs.
func (b *ircNetwork) handleChanStatsCommand(target, sender, message string) {
	channel, days := "", 7
	if strings.HasPrefix(target, "#") || strings.HasPrefix(target, "&") {
		channel = target
	}
	for _, f := range strings.Fields(message)[1:] {
		if n, err := strconv.Atoi(f); err == nil {
			days = n
		} else if strings.HasPrefix(f, "#") || strings.HasPrefix(f, "&") {
			channel = f
		}
	}
	if channel == "" {
		b.sendPrivmsg(target, fmt.Sprintf("Usage: %schanstats [#channel] [days]", b.pfx()))
		return
	}
	days = min(max(days, 1), chanStatsMaxDays)
	a := logger.ChannelActivity(b.name, channel, days, b.netCfg().Nickname)
	b.sendPrivmsgMentionedLines(target, sender, chanStatsLines(channel, days, a)...)
}
