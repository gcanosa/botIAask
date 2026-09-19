package stats

import (
	"encoding/json"
	"sort"
	"time"

	"botIAask/logger"
)

type NameCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type ChanSummary struct {
	Network  string `json:"network"`
	Channel  string `json:"channel"`
	Msgs     int    `json:"msgs"`
	Actions  int    `json:"actions"`
	Joins    int    `json:"joins"`
	Parts    int    `json:"parts"`
	Topics   int    `json:"topics"`
	PeakHour int    `json:"peak_hour"`
	hours    [24]int
}

type DayPoint struct {
	Day     string `json:"day"`
	Msgs    int    `json:"msgs"`
	Actions int    `json:"actions"`
	Joins   int    `json:"joins"`
	Parts   int    `json:"parts"`
	Topics  int    `json:"topics"`
}

type TopicEntry struct {
	Day  string `json:"day"`
	Time string `json:"time"`
	Nick string `json:"nick"`
	Text string `json:"text"`
}

// ChanReport is everything the Channel Stats page needs in one response.
type ChanReport struct {
	Days     int            `json:"days"`
	Channels []ChanSummary  `json:"channels"` // every channel in range (for the comparison cards)
	Hours    [24]int        `json:"hours"`    // selected channel(s), messages+actions by hour
	Heat     [7][24]int     `json:"heat"`     // [weekday Sun..Sat][hour]
	Daily    []DayPoint     `json:"daily"`    // continuous, zero-filled
	Nicks    []NameCount    `json:"nicks"`    // merged per-day top lists (approximate for wide ranges)
	Cmds     []NameCount    `json:"cmds"`     // command usage
	Modes    map[string]int `json:"modes"`    // "+o": n
	ModeBy   []NameCount    `json:"mode_by"`  // who sets modes
	Topics   []TopicEntry   `json:"topics"`   // newest first, capped
}

func sortedCounts(m map[string]int, n int) []NameCount {
	out := make([]NameCount, 0, len(m))
	for k, v := range m {
		out = append(out, NameCount{k, v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// ChanReport aggregates the last `days` days. network/channel select the detail view; both
// empty means every channel combined. Summaries always cover all channels.
func (d *Database) ChanReport(days int, network, channel string) (ChanReport, error) {
	rep := ChanReport{Days: days, Modes: map[string]int{}, Channels: []ChanSummary{}, Topics: []TopicEntry{}}
	now := time.Now()
	since := now.AddDate(0, 0, -(days - 1)).Format("2006-01-02")
	rows, err := d.db.Query(`SELECT network, channel, day, msgs, actions, joins, parts, topics,
		hours, nicks, cmds, modes, mode_by, topic_log FROM chan_activity WHERE day >= ? ORDER BY day`, since)
	if err != nil {
		return rep, err
	}
	defer rows.Close()

	sums := map[string]*ChanSummary{}
	daily := map[string]*DayPoint{}
	nicks, cmds, modeBy := map[string]int{}, map[string]int{}, map[string]int{}
	for rows.Next() {
		var n, c, day string
		var msgs, actions, joins, parts, topics int
		var hoursJ, nicksJ, cmdsJ, modesJ, modeByJ, topicJ string
		if err := rows.Scan(&n, &c, &day, &msgs, &actions, &joins, &parts, &topics, &hoursJ, &nicksJ, &cmdsJ, &modesJ, &modeByJ, &topicJ); err != nil {
			return rep, err
		}
		var hours [24]int
		_ = json.Unmarshal([]byte(hoursJ), &hours)

		s := sums[n+"\x00"+c]
		if s == nil {
			s = &ChanSummary{Network: n, Channel: c}
			sums[n+"\x00"+c] = s
		}
		s.Msgs += msgs
		s.Actions += actions
		s.Joins += joins
		s.Parts += parts
		s.Topics += topics
		for h, v := range hours {
			s.hours[h] += v
		}

		if (network != "" && n != network) || (channel != "" && c != channel) {
			continue
		}
		p := daily[day]
		if p == nil {
			p = &DayPoint{Day: day}
			daily[day] = p
		}
		p.Msgs += msgs
		p.Actions += actions
		p.Joins += joins
		p.Parts += parts
		p.Topics += topics
		t, _ := time.ParseInLocation("2006-01-02", day, time.Local)
		for h, v := range hours {
			rep.Hours[h] += v
			rep.Heat[t.Weekday()][h] += v
		}
		merge := func(js string, into map[string]int) {
			m := map[string]int{}
			if json.Unmarshal([]byte(js), &m) == nil {
				for k, v := range m {
					into[k] += v
				}
			}
		}
		merge(nicksJ, nicks)
		merge(cmdsJ, cmds)
		merge(modesJ, rep.Modes)
		merge(modeByJ, modeBy)
		var tl []logger.TopicChange
		if json.Unmarshal([]byte(topicJ), &tl) == nil {
			for _, x := range tl {
				rep.Topics = append(rep.Topics, TopicEntry{day, x.Time, x.Nick, x.Text})
			}
		}
	}
	if err := rows.Err(); err != nil {
		return rep, err
	}

	for _, s := range sums {
		for h, v := range s.hours {
			if v > s.hours[s.PeakHour] {
				s.PeakHour = h
			}
		}
		rep.Channels = append(rep.Channels, *s)
	}
	sort.Slice(rep.Channels, func(i, j int) bool {
		if rep.Channels[i].Msgs != rep.Channels[j].Msgs {
			return rep.Channels[i].Msgs > rep.Channels[j].Msgs
		}
		return rep.Channels[i].Channel < rep.Channels[j].Channel
	})
	for i := days - 1; i >= 0; i-- {
		day := now.AddDate(0, 0, -i).Format("2006-01-02")
		if p := daily[day]; p != nil {
			rep.Daily = append(rep.Daily, *p)
		} else {
			rep.Daily = append(rep.Daily, DayPoint{Day: day})
		}
	}
	rep.Nicks, rep.Cmds, rep.ModeBy = sortedCounts(nicks, 15), sortedCounts(cmds, 15), sortedCounts(modeBy, 5)
	sort.SliceStable(rep.Topics, func(i, j int) bool {
		if rep.Topics[i].Day != rep.Topics[j].Day {
			return rep.Topics[i].Day > rep.Topics[j].Day
		}
		return rep.Topics[i].Time > rep.Topics[j].Time
	})
	if len(rep.Topics) > 20 {
		rep.Topics = rep.Topics[:20]
	}
	return rep, nil
}
