package irc

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"botIAask/weather"
)

// mIRC-style formatting (bold + fg/bg); see https://modern.ircdocs.horse/formatting.html
const (
	ircBold = "\x02"
	ircCol  = "\x03"
	ircEnd  = "\x03" // reset color
	// 16-color: 1=black bg for both; 4=red fg (max), 12=light blue fg (min)
	ircMaxTemp = ircCol + "04,01" // red on black
	ircMinTemp = ircCol + "12,01" // blue on black
)

var weatherHTTP = &http.Client{Timeout: 22 * time.Second}

func ircBoldLabel(s string) string {
	return ircBold + s + ircBold
}

// ircFormatMaxTemp wraps a max value (°C) with red-on-black; includes degree sign inside the color block.
func ircFormatMaxTemp(v float64) string {
	return ircMaxTemp + fmt.Sprintf("%.0f°", v) + ircEnd
}

// ircFormatMinTemp wraps a min value (°C) with blue-on-black.
func ircFormatMinTemp(v float64) string {
	return ircMinTemp + fmt.Sprintf("%.0f°", v) + ircEnd
}

// handleWeatherCommand fetches a forecast for query (e.g. "Barcelona, Spain") via Open-Meteo.
func (b *Bot) handleWeatherCommand(target, sender, query string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	snap, err := weather.FetchSnapshot(ctx, weatherHTTP, query)
	if err != nil {
		b.sendPrivmsg(target, b.sanitize(fmt.Sprintf("@%s: Weather: %v", sender, err)))
		return
	}
	if snap == nil || !snap.OK {
		msg := "unavailable"
		if snap != nil && snap.Message != "" {
			msg = snap.Message
		}
		b.sendPrivmsg(target, b.sanitize(fmt.Sprintf("@%s: Weather: %s", sender, msg)))
		return
	}
	line1, line2 := formatWeatherIRCLines(snap)
	if line2 != "" {
		b.sendPrivmsg(target, b.sanitize(line1))
		b.sendPrivmsg(target, b.sanitize(line2))
	} else {
		b.sendPrivmsg(target, b.sanitize(line1))
	}
}

func formatWeatherIRCLines(s *weather.Snapshot) (line1, line2 string) {
	if s == nil || s.Current == nil {
		return "\x0303,01[WEATHER]\x03 —", ""
	}
	c := s.Current
	cur := fmt.Sprintf(
		"\x0303,01[WEATHER]\x03 %s — %.0f°C %s · %s %.0f km/h",
		s.Location, c.TempC, c.Summary, ircBoldLabel("wind"), c.WindKmh,
	)
	if math.Abs(c.ApparentC-c.TempC) >= 0.5 {
		cur += fmt.Sprintf(" · %s %.0f°C", ircBoldLabel("feels"), c.ApparentC)
	}
	if c.Humidity > 0 && c.Humidity <= 100 {
		cur += fmt.Sprintf(" · %s %d%%", ircBoldLabel("humidity"), c.Humidity)
	}
	// Today’s high/low (same mIRC colors as the 5-day strip; daily[0] is local “today”).
	if len(s.Daily) > 0 {
		t0 := s.Daily[0]
		cur += " · " + ircBoldLabel("today:") + " "
		cur += ircFormatMaxTemp(t0.MaxC)
		cur += "/"
		cur += ircFormatMinTemp(t0.MinC)
	}
	if len(s.Daily) == 0 {
		return cur, ""
	}
	var sb strings.Builder
	for i, d := range s.Daily {
		if i > 0 {
			sb.WriteString(" · ")
		}
		day := strings.TrimSpace(d.Weekday)
		if day == "" {
			day = d.Date
		}
		// max°C first (red on black) / min°C (blue on black), slash between
		sb.WriteString(day)
		sb.WriteString(" ")
		sb.WriteString(ircFormatMaxTemp(d.MaxC))
		sb.WriteString("/")
		sb.WriteString(ircFormatMinTemp(d.MinC))
	}
	daily := ircBoldLabel("5d:") + " " + sb.String() + " (open-meteo.com)"
	full := cur + " | " + daily
	if len([]byte(full)) <= 450 {
		return full, ""
	}
	return cur, daily
}
