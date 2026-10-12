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

// dewPointC is the Magnus formula (Alduchov–Eskridge constants), accurate to ~0.4°C
// over -40…50°C at any RH (the old T-(100-RH)/5 rule drifts badly below 50% RH).
func dewPointC(tempC float64, humidity int) float64 {
	const b, c = 17.625, 243.04
	g := math.Log(float64(humidity)/100) + b*tempC/(c+tempC)
	return c * g / (b - g)
}

// barCells rounds pct (0–100) to the nearest of width cells.
func barCells(pct, width int) int {
	return (pct*width + 50) / 100
}

// humidityBar returns a visual progress bar colored by dewpoint (comfort level).
func humidityBar(humidity int, tempC float64) string {
	const width = 10
	dew := dewPointC(tempC, humidity)

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

	filled := barCells(humidity, width)
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

	filled := barCells(pct, width)
	bar := "[" + color + strings.Repeat("▓", filled) + ircEnd +
		ircGray + strings.Repeat("░", width-filled) + ircEnd + "]"

	return fmt.Sprintf("%s %.0f°C (↓%.0f° ↑%.0f°)", bar, tempC, minC, maxC)
}

// handleWeatherCommand fetches a forecast for query (e.g. "Barcelona, Spain") via Open-Meteo.
func (b *ircNetwork) handleWeatherCommand(target, sender, query string) {
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
	line1, line2 := formatWeatherIRCLines(snap, weatherTextBudget(b.netCfg().Nickname, target))
	if line2 != "" {
		b.sendPrivmsg(target, b.sanitize(line1))
		b.sendPrivmsg(target, b.sanitize(line2))
	} else {
		b.sendPrivmsg(target, b.sanitize(line1))
	}
}

// weatherIcon is a condition symbol for an IconKind; BMP text-presentation glyphs where
// one exists (3 bytes, single-width in terminals), emoji only for fog/partly/thunder.
func weatherIcon(kind string) string {
	switch kind {
	case "clear":
		return "☀"
	case "night_clear":
		return "☾"
	case "partly":
		return "⛅"
	case "cloud":
		return "☁"
	case "fog":
		return "🌫"
	case "rain", "shower":
		return "☂"
	case "snow":
		return "❄"
	case "thunder":
		return "⚡"
	}
	return ""
}

// weatherTextBudget is the PRIVMSG text bytes that survive relay: the server prepends
// ":nick!user@host PRIVMSG target :" and caps the line at 512 incl. CRLF. We don't know
// our user@host, so assume the worst (10-byte ident, 63-byte host).
func weatherTextBudget(nick, target string) int {
	n := 512 - len("\r\n:!@ PRIVMSG  :") - len(nick) - 10 - 63 - len(target)
	return min(n, ircTextBudget)
}

func formatWeatherIRCLines(s *weather.Snapshot, budget int) (line1, line2 string) {
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
	summary := c.Summary
	if ic := weatherIcon(c.Icon); ic != "" {
		summary = ic + " " + summary
	}
	cur := fmt.Sprintf(
		"\x0303,01[WEATHER]\x03 %s — %s %s · %s %.0f km/h",
		s.Location, tempStr, summary, ircBoldLabel("wind"), c.WindKmh,
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
	// Prefer one line: 5-day forecast, then 3-day; only split if even 3 days won't fit.
	for _, n := range []int{5, 3} {
		if full := cur + " | " + dailyForecast(s.Daily, n); len(full) <= budget {
			return full, ""
		}
	}
	return cur, dailyForecast(s.Daily, 5)
}

// dailyForecast renders up to n days as "Nd: Mon ☀24°/15° · …" (max red, min blue).
func dailyForecast(days []weather.Day, n int) string {
	days = days[:min(n, len(days))]
	var sb strings.Builder
	for i, d := range days {
		if i > 0 {
			sb.WriteString(" · ")
		}
		day := strings.TrimSpace(d.Weekday)
		if day == "" {
			day = d.Date
		}
		sb.WriteString(day)
		sb.WriteString(" ")
		sb.WriteString(weatherIcon(d.Icon))
		sb.WriteString(ircFormatMaxTemp(d.MaxC))
		sb.WriteString("/")
		sb.WriteString(ircFormatMinTemp(d.MinC))
	}
	return ircBoldLabel(fmt.Sprintf("%dd:", len(days))) + " " + sb.String()
}
