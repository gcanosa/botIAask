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
	// Semaphore / heat colors for progress bars
	ircGreen      = ircCol + "03,01" // green on black (dewpoint comfortable / cool temps)
	ircYellow     = ircCol + "08,01" // yellow on black (dewpoint slightly muggy / warm temps)
	ircOrange     = ircCol + "07,01" // orange on black (dewpoint muggy / hot temps)
	ircRed        = ircCol + "04,01" // red on black (dewpoint oppressive / very hot)
	ircLightBlue  = ircCol + "12,01" // light-blue on black (freezing)
	ircLightCyan  = ircCol + "11,01" // light-cyan on black (cold)
	ircLightGreen = ircCol + "09,01" // light-green on black (cool/mild)
	ircGray       = ircCol + "14,01" // gray on black (bar track/empty)
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

// humidityBar returns a visual progress bar colored by dewpoint (comfort level).
// Uses simplified Magnus: Td = T - ((100 - RH) / 5)
func humidityBar(humidity int, tempC float64) string {
	const width = 10
	dew := tempC - float64(100-humidity)/5.0

	var color string
	switch {
	case dew < 10:
		color = ircGreen
	case dew < 16:
		color = ircYellow
	case dew < 21:
		color = ircOrange
	default:
		color = ircRed
	}

	filled := (humidity*width + 99) / 100
	bar := "[" + color + strings.Repeat("▓", filled) + ircEnd +
		ircGray + strings.Repeat("░", width-filled) + ircEnd + "]"

	return fmt.Sprintf("%s %d%% dew %d°C", bar, humidity, int(math.Round(dew)))
}

// tempBar returns a visual progress bar showing current temp within today's min/max range,
// colored by absolute temperature (heat scale).
func tempBar(tempC, minC, maxC float64) string {
	const width = 10
	span := maxC - minC
	var pct int
	if span > 0 {
		pct = int(math.Round((tempC - minC) / span * 100))
	}
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}

	var color string
	switch {
	case tempC < 0:
		color = ircLightBlue
	case tempC < 10:
		color = ircLightCyan
	case tempC < 20:
		color = ircLightGreen
	case tempC < 28:
		color = ircYellow
	case tempC < 35:
		color = ircOrange
	default:
		color = ircRed
	}

	filled := (pct*width + 99) / 100
	bar := "[" + color + strings.Repeat("▓", filled) + ircEnd +
		ircGray + strings.Repeat("░", width-filled) + ircEnd + "]"

	return fmt.Sprintf("%s %.0f°C (%.0f°–%.0f°)", bar, tempC, minC, maxC)
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
	var tempStr string
	if len(s.Daily) > 0 {
		tempStr = tempBar(c.TempC, s.Daily[0].MinC, s.Daily[0].MaxC)
	} else {
		tempStr = fmt.Sprintf("%.0f°C", c.TempC)
	}
	cur := fmt.Sprintf(
		"\x0303,01[WEATHER]\x03 %s — %s %s · %s %.0f km/h",
		s.Location, tempStr, c.Summary, ircBoldLabel("wind"), c.WindKmh,
	)
	if math.Abs(c.ApparentC-c.TempC) >= 0.5 {
		cur += fmt.Sprintf(" · %s %.0f°C", ircBoldLabel("feels"), c.ApparentC)
	}
	if c.Humidity > 0 && c.Humidity <= 100 {
		cur += " · " + ircBoldLabel("humidity") + " " + humidityBar(c.Humidity, c.TempC)
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
