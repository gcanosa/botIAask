package flight

import (
	"fmt"
	"strings"
	"time"
)

// mIRC color/formatting; see irc/weather_cmd.go
const (
	ircBold  = "\x02"
	ircCol   = "\x03"
	ircEnd   = "\x03"
	ircHdr   = ircCol + "03,01"
	ircPos   = ircCol + "03,01"
	ircTrack = ircCol + "14,01"
	ircBad   = ircCol + "04,01"
)

// FormatIRCLines turns a successful Snapshot into lines for PRIVMSG (caller sanitizes per line).
func FormatIRCLines(s *Snapshot, now time.Time) []string {
	if s == nil {
		return []string{ircHdr + "[FLIGHT]" + ircEnd + " — no data"}
	}
	if !s.OK {
		return nil
	}
	now = now.UTC()

	bold := func(t string) string { return ircBold + t + ircBold }

	flightID := s.FlightIATA
	if flightID == "" {
		flightID = "?"
	}
	airline := strings.TrimSpace(s.AirlineName)
	if s.AirlineIATA != "" {
		if airline != "" {
			airline = s.AirlineIATA + " · " + airline
		} else {
			airline = s.AirlineIATA
		}
	}
	origin := endpointDisplay(&s.Dep)
	dest := endpointDisplay(&s.Arr)
	line1 := ircHdr + "[FLIGHT]" + ircEnd + " " + flightID
	if airline != "" {
		line1 += " · " + airline
	}
	line1 += " — " + origin + " → " + dest

	phase := PhaseLabel(s.Status)
	if s.Status == "cancelled" {
		phase = ircBad + phase + ircEnd
	} else if s.Status == "landed" {
		phase = ircPos + phase + ircEnd
	}
	stLine := "St: " + phase
	stLine += " · " + DelayTagLine(s.Dep.Delay, s.Arr.Delay)

	schedD, haveSched := BlockDuration(&s.Dep, &s.Arr)
	if haveSched {
		stLine += " | sched " + FormatDurationHrsMin(schedD)
	}
	if s.DurationMin > 0 && !haveSched {
		stLine += " | sched ~" + FormatDurationHrsMin(time.Duration(s.DurationMin)*time.Minute)
	}
	if eb, ok := ElapsedBlock(&s.Dep, &s.Arr, s.Status); ok {
		if !haveSched || (schedD > 0 && (eb > schedD+2*time.Minute || eb+2*time.Minute < schedD)) {
			stLine += " | now " + FormatDurationHrsMin(eb)
		}
	}

	// Local times: prefer API local strings, else scheduled in airport TZ
	if strings.TrimSpace(s.Dep.LocalStr) != "" {
		stLine += " | " + bold("dep") + " " + strings.TrimSpace(s.Dep.LocalStr) + " loc"
	} else if s.Dep.Scheduled != nil {
		stLine += " | " + bold("dep") + " " + FormatLocalInZone(s.Dep.Scheduled, s.Dep.Timezone) + " loc"
	}
	if strings.TrimSpace(s.Arr.LocalStr) != "" {
		stLine += " | " + bold("arr") + " " + strings.TrimSpace(s.Arr.LocalStr) + " loc"
	} else if s.Arr.Scheduled != nil {
		stLine += " | " + bold("arr") + " " + FormatLocalInZone(s.Arr.Scheduled, s.Arr.Timezone) + " loc"
	}
	if s.Dep.Actual != nil {
		stLine += " · " + bold("act dep") + " " + FormatLocalInZone(s.Dep.Actual, s.Dep.Timezone)
	}
	switch s.Status {
	case "landed":
		if s.Arr.Actual != nil {
			stLine += " · " + bold("act arr") + " " + FormatLocalInZone(s.Arr.Actual, s.Arr.Timezone)
		}
	case "active":
		if s.Arr.Estimated != nil {
			stLine += " · " + bold("est arr") + " " + FormatLocalInZone(s.Arr.Estimated, s.Arr.Timezone)
		}
	default:
		if s.Arr.Estimated != nil && s.Arr.Scheduled != nil && !s.Arr.Estimated.Equal(*s.Arr.Scheduled) {
			stLine += " · " + bold("est arr") + " " + FormatLocalInZone(s.Arr.Estimated, s.Arr.Timezone)
		} else if s.Arr.Actual != nil {
			stLine += " · " + bold("act arr") + " " + FormatLocalInZone(s.Arr.Actual, s.Arr.Timezone)
		}
	}

	var ex []string
	if s.Aircraft != "" {
		ex = append(ex, bold("ac")+" "+strings.TrimSpace(s.Aircraft))
	}
	tg := gateTerminal(&s.Dep)
	if tg != "" {
		ex = append(ex, bold("dep")+" "+tg)
	}
	tg = gateTerminal(&s.Arr)
	if tg != "" {
		ex = append(ex, bold("arr")+" "+tg)
	}
	if s.Arr.Baggage != "" {
		ex = append(ex, bold("bag")+" "+s.Arr.Baggage)
	}
	if s.Codeshare != "" {
		ex = append(ex, s.Codeshare)
	}
	if s.Live.HasData {
		if s.Live.AltM != nil {
			if *s.Live.AltM >= 1000 {
				ex = append(ex, fmt.Sprintf("alt %dkm", int(*s.Live.AltM/1000+0.5)))
			} else {
				ex = append(ex, fmt.Sprintf("alt %.0fm", *s.Live.AltM))
			}
		}
		if s.Live.SpeedKmh != nil {
			ex = append(ex, fmt.Sprintf("%.0f km/h gnd", *s.Live.SpeedKmh))
		}
	}
	exStr := strings.Join(ex, " · ")

	prog := ComputeProgress(s.Status, now, &s.Dep, &s.Arr)
	var line3 string
	if prog.Known && prog.ShowBar {
		line3 = ircColoredBar(prog.Percent, 10) + fmt.Sprintf(" %d%%", prog.Percent)
	} else if prog.Known && !prog.ShowBar {
		line3 = "Trip: n/a (cancelled or diverted)"
	} else {
		line3 = "Progress: --"
	}
	src := s.APIAttribution
	if src == "" {
		src = "airlabs.co"
	}
	lines := []string{line1, stLine}
	if exStr != "" {
		lines = append(lines, exStr)
	}
	lines = append(lines, line3+" ("+src+")")
	return lines
}

func gateTerminal(leg *endpointLeg) string {
	if leg == nil {
		return ""
	}
	parts := []string{}
	if leg.Terminal != "" {
		parts = append(parts, "T"+leg.Terminal)
	}
	if leg.Gate != "" {
		parts = append(parts, "g"+leg.Gate)
	}
	return strings.Join(parts, " ")
}

func ircColoredBar(percent, width int) string {
	if width < 4 {
		width = 4
	}
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	filled := (percent*width + 99) / 100
	if filled > width {
		filled = width
	}
	return ircPos + strings.Repeat("▓", filled) + ircEnd + ircTrack + strings.Repeat("░", width-filled) + ircEnd
}
