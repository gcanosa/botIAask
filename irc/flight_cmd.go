package irc

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"botIAask/flight"
)

var (
	flightHTTP    = &http.Client{Timeout: 25 * time.Second}
	flightYMD     = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

func (b *Bot) handleFlightCommand(target, sender, rest string) {
	parts := strings.Fields(rest)
	if len(parts) < 1 {
		b.sendPrivmsg(target, fmt.Sprintf("Usage: %sflight <IATA> [YYYY-MM-DD] — e.g. %sflight AA100 (AirLabs v9; set flight.api_key or AIRLABS_API_KEY; paid = higher daily quota; date optional)", b.prefix, b.prefix))
		return
	}
	fid := strings.ToUpper(strings.TrimSpace(parts[0]))
	var flightDate *time.Time
	if len(parts) > 1 && flightYMD.MatchString(parts[1]) {
		t, err := time.ParseInLocation("2006-01-02", parts[1], time.Local)
		if err == nil {
			flightDate = &t
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 22*time.Second)
	defer cancel()
	p := flight.FetchParams{
		APIKey:   b.cfg.Flight.AirLabsAPIKeyOrEnv(),
		BaseURL:  b.cfg.Flight.BaseURL,
		FlightID: fid,
		HTTP:     flightHTTP,
	}
	_ = flightDate
	snap, err := flight.Fetch(ctx, p)
	if err != nil {
		b.sendPrivmsg(target, b.sanitize(fmt.Sprintf("@%s: flight: %v", sender, err)))
		return
	}
	if snap == nil || !snap.OK {
		msg := "unavailable"
		if snap != nil && snap.Error != "" {
			msg = snap.Error
		}
		b.sendPrivmsg(target, b.sanitize(fmt.Sprintf("@%s: flight: %s", sender, msg)))
		return
	}
	lines := flight.FormatIRCLines(snap, time.Now())
	if len(lines) == 0 {
		return
	}
	b.sendPrivmsg(target, b.sanitize(fmt.Sprintf("@%s: %s", sender, lines[0])))
	for i := 1; i < len(lines); i++ {
		b.sendPrivmsg(target, b.sanitize(lines[i]))
	}
}
