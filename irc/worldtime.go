package irc

import (
	"fmt"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // zone data in the binary: slim container images may not ship /usr/share/zoneinfo
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

// worldTimeZones loads each distinct zone once (zoneinfo is parsed from disk on every LoadLocation);
// zones that fail to load are left out.
var worldTimeZones = sync.OnceValue(func() []struct {
	label string
	loc   *time.Location
} {
	var out []struct {
		label string
		loc   *time.Location
	}
	seen := make(map[string]struct{})
	for _, c := range worldTimeCities {
		if _, dup := seen[c.Location]; dup {
			continue
		}
		seen[c.Location] = struct{}{}
		if loc, err := time.LoadLocation(c.Location); err == nil {
			out = append(out, struct {
				label string
				loc   *time.Location
			}{c.Label, loc})
		}
	}
	return out
})

// handleTimeCommand shows current local times for major world cities, one per IANA zone.
func (b *ircNetwork) handleTimeCommand(target string) {
	now := time.Now()
	var parts []string
	for _, c := range worldTimeZones() {
		parts = append(parts, fmt.Sprintf("%s %s", c.label, now.In(c.loc).Format("Mon 2 15:04:05 MST 2006")))
	}
	msg := "World time: " + strings.Join(parts, " | ")
	for _, chunk := range splitUTF8ByByteBudget(msg, ircTextBudget) {
		if chunk == "" {
			continue
		}
		b.sendPrivmsg(target, b.sanitize(chunk))
	}
}
