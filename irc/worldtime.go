package irc

import (
	"fmt"
	"strings"
	"time"
)

// worldTimeCities is west→east. Each IANA location appears at most once; labels are the display names.
var worldTimeCities = []struct {
	Label    string
	Location string
}{
	{"Honolulu", "Pacific/Honolulu"},
	{"Los Angeles", "America/Los_Angeles"},
	{"Chicago", "America/Chicago"},
	{"New York", "America/New_York"},
	{"Mexico City", "America/Mexico_City"},
	{"São Paulo", "America/Sao_Paulo"},
	{"Buenos Aires, Argentina", "America/Argentina/Buenos_Aires"},
	{"London", "Europe/London"},
	{"Barcelona, Spain", "Europe/Madrid"},
	{"Dubai", "Asia/Dubai"},
	{"Mumbai", "Asia/Kolkata"},
	{"Singapore", "Asia/Singapore"},
	{"Shanghai", "Asia/Shanghai"},
	{"Tokyo", "Asia/Tokyo"},
	{"Sydney", "Australia/Sydney"},
	{"Auckland", "Pacific/Auckland"},
}

// handleTimeCommand shows current local times for major world cities, one per IANA zone.
func (b *Bot) handleTimeCommand(target string) {
	now := time.Now()
	seen := make(map[string]struct{})
	var parts []string
	for _, c := range worldTimeCities {
		if _, dup := seen[c.Location]; dup {
			continue
		}
		seen[c.Location] = struct{}{}
		loc, err := time.LoadLocation(c.Location)
		if err != nil {
			continue
		}
		t := now.In(loc)
		parts = append(parts, fmt.Sprintf("%s %s", c.Label, t.Format("Mon 2 15:04:05 MST 2006")))
	}
	msg := "World time: " + strings.Join(parts, " | ")
	for _, chunk := range splitUTF8ByByteBudget(msg, ircTextBudget) {
		if chunk == "" {
			continue
		}
		b.sendPrivmsg(target, b.sanitize(chunk))
	}
}
