package logger

import (
	"bufio"
	"io"
	"regexp"
	"strings"
)

// TopicChange is one topic edit seen in a log.
type TopicChange struct {
	Time string `json:"t"` // HH:MM
	Nick string `json:"n"`
	Text string `json:"x"`
}

// DayRollup is one channel-day of activity, parsed from a daily log.
type DayRollup struct {
	Msgs, Actions, Joins, Parts, Topics int
	Hours                               [24]int        // messages+actions by hour (bot local time)
	Nicks                               map[string]int // talkers
	Cmds                                map[string]int // "!cmd" usage, keyed by lowercase name without prefix
	Modes                               map[string]int // "+o", "-v", ... (q a o h v b only)
	ModeBy                              map[string]int // who set modes
	TopicLog                            []TopicChange  // capped
}

const maxTopicLog = 5

var (
	reMsg    = regexp.MustCompile(`^\[(\d\d):\d\d:\d\d\] <([^>]+)> (.*)$`)
	reAction = regexp.MustCompile(`^\[(\d\d):\d\d:\d\d\] \* (\S+) `)
	reJoin   = regexp.MustCompile(`^\[\d\d:\d\d:\d\d\] \*\*\* \S+ has joined `)
	rePart   = regexp.MustCompile(`^\[\d\d:\d\d:\d\d\] \*\*\* \S+ has left `)
	reMode   = regexp.MustCompile(`^\[\d\d:\d\d:\d\d\] \*\*\* (\S+) sets mode (\S+)`)
	reTopic  = regexp.MustCompile(`^\[(\d\d:\d\d):\d\d\] \*\*\* (\S+) changed the topic to: (.*)$`)
	reCmd    = regexp.MustCompile(`^([A-Za-z0-9]{1,24})\b`)
)

// ParseDayLog aggregates one daily log. Lines from skipNick (the bot) are ignored so
// announcements don't drown out real chatter; prefix is the command prefix (e.g. "!").
func ParseDayLog(r io.Reader, skipNick, prefix string) DayRollup {
	d := DayRollup{Nicks: map[string]int{}, Cmds: map[string]int{}, Modes: map[string]int{}, ModeBy: map[string]int{}}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if m := reMsg.FindStringSubmatch(line); m != nil {
			if strings.EqualFold(m[2], skipNick) {
				continue
			}
			d.count(m[1], m[2], false)
			if prefix != "" && strings.HasPrefix(m[3], prefix) {
				if c := reCmd.FindStringSubmatch(m[3][len(prefix):]); c != nil {
					d.Cmds[strings.ToLower(c[1])]++
				}
			}
		} else if m := reAction.FindStringSubmatch(line); m != nil {
			if !strings.EqualFold(m[2], skipNick) {
				d.count(m[1], m[2], true)
			}
		} else if m := reMode.FindStringSubmatch(line); m != nil {
			sign := byte('+')
			for i := 0; i < len(m[2]); i++ {
				switch c := m[2][i]; c {
				case '+', '-':
					sign = c
				case 'q', 'a', 'o', 'h', 'v', 'b':
					d.Modes[string(sign)+string(c)]++
					d.ModeBy[m[1]]++
				}
			}
		} else if m := reTopic.FindStringSubmatch(line); m != nil {
			d.Topics++
			if len(d.TopicLog) < maxTopicLog {
				txt := m[3]
				if len(txt) > 160 {
					txt = txt[:160] + "…"
				}
				d.TopicLog = append(d.TopicLog, TopicChange{m[1], m[2], txt})
			}
		} else if reJoin.MatchString(line) {
			d.Joins++
		} else if rePart.MatchString(line) {
			d.Parts++
		}
	}
	return d
}

func (d *DayRollup) count(hh, nick string, action bool) {
	h := int(hh[0]-'0')*10 + int(hh[1]-'0')
	if h > 23 {
		return
	}
	if action {
		d.Actions++
	} else {
		d.Msgs++
	}
	d.Hours[h]++
	d.Nicks[nick]++
}
