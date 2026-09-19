package logger

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Activity is a channel's message volume aggregated from its daily log files.
type Activity struct {
	Total   int
	Hours   [24]int // by hour of day (bot local time, as written by LogChannelEvent)
	Weekday [7]int  // by time.Weekday
	Nicks   map[string]int
	Days    int // number of days that had a log file
}

var (
	msgLine    = regexp.MustCompile(`^\[(\d\d):\d\d:\d\d\] <([^>]+)> `)
	actionLine = regexp.MustCompile(`^\[(\d\d):\d\d:\d\d\] \* (\S+) `)
)

// ChannelActivity reads the last `days` plain-text daily logs for channel on serverName.
// Messages and actions from skipNick (the bot itself) are ignored. Archived .gz logs are not read.
// ponytail: plain logs only, add logs/archive/*.gz reading if --days should outlive log rotation.
func ChannelActivity(serverName, channel string, days int, skipNick string) Activity {
	a := Activity{Nicks: map[string]int{}}
	key := ChannelFileKey(channel, serverName)
	now := time.Now()
	for i := 0; i < days; i++ {
		d := now.AddDate(0, 0, -i)
		f, err := os.Open(filepath.Join(logsDir, key+"_"+d.Format("2006-01-02")+".log"))
		if err != nil {
			continue
		}
		a.Days++
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			m := msgLine.FindStringSubmatch(line)
			if m == nil {
				if m = actionLine.FindStringSubmatch(line); m == nil {
					continue
				}
			}
			if strings.EqualFold(m[2], skipNick) {
				continue
			}
			h := int(m[1][0]-'0')*10 + int(m[1][1]-'0')
			if h > 23 {
				continue
			}
			a.Total++
			a.Hours[h]++
			a.Weekday[d.Weekday()]++
			a.Nicks[m[2]]++
		}
		f.Close()
	}
	return a
}
