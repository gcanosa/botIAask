package irc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // zone data in the binary: slim container images may not ship /usr/share/zoneinfo

	"botIAask/internal/httpx"
	"botIAask/internal/ttlcache"
	"botIAask/meta"
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
		parts = append(parts, fmt.Sprintf("%s %s%s%s", ircBoldLabel(c.label), ircYellow, now.In(c.loc).Format("Mon 2 15:04:05 MST 2006"), ircEnd))
	}
	msg := "\x0303,01[TIME]\x03 " + strings.Join(parts, " · ")
	for _, chunk := range splitUTF8ByByteBudget(msg, ircTextBudget) {
		if chunk == "" {
			continue
		}
		b.sendPrivmsg(target, b.sanitize(chunk))
	}
}

// Open-Meteo (same as !weather, no key): geocoding gives cities an IANA zone; countries come back
// without one, so their centroid is resolved through the forecast endpoint's timezone=auto.
// ponytail: a multi-zone country (US, Russia, Brazil) shows its centroid's zone; name a city for others.
var (
	timeGeoAPI = "https://geocoding-api.open-meteo.com"
	timeTZAPI  = "https://api.open-meteo.com"
)

// timePlaceCache maps a lowered query to its display label and IANA zone (places don't move).
var timePlaceCache = ttlcache.Cache[string, [2]string]{TTL: 24 * time.Hour, Max: 512}

var errTimeNoPlace = errors.New("not found")

func timeGetJSON(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", meta.Name+"/"+meta.Version+" (IRC bot)")
	resp, err := weatherHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return httpx.DecodeJSON(resp.Body, httpx.MaxBody, v)
}

// resolveTimePlace returns a display label and IANA zone name for a city or country.
func resolveTimePlace(ctx context.Context, query string) (label, zone string, err error) {
	key := strings.ToLower(query)
	if v, ok := timePlaceCache.Get(key); ok {
		return v[0], v[1], nil
	}
	var g struct {
		Results []struct {
			Name      string  `json:"name"`
			Admin1    string  `json:"admin1"`
			Country   string  `json:"country"`
			Timezone  string  `json:"timezone"`
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
		} `json:"results"`
	}
	u := timeGeoAPI + "/v1/search?" + url.Values{"name": {query}, "count": {"1"}, "language": {"en"}}.Encode()
	if err := timeGetJSON(ctx, u, &g); err != nil {
		return "", "", err
	}
	if len(g.Results) == 0 {
		return "", "", errTimeNoPlace
	}
	r := g.Results[0]
	label = r.Name
	for _, part := range []string{r.Admin1, r.Country} {
		if part != "" && part != r.Name && !strings.HasSuffix(label, ", "+part) {
			label += ", " + part
		}
	}
	zone = r.Timezone
	if zone == "" {
		var f struct {
			Timezone string `json:"timezone"`
		}
		u := fmt.Sprintf("%s/v1/forecast?latitude=%g&longitude=%g&timezone=auto", timeTZAPI, r.Latitude, r.Longitude)
		if err := timeGetJSON(ctx, u, &f); err != nil {
			return "", "", err
		}
		zone = f.Timezone
	}
	if zone == "" {
		return "", "", errTimeNoPlace
	}
	timePlaceCache.Put(key, [2]string{label, zone})
	return label, zone, nil
}

// handleTimePlaceCommand answers !time <city|country> with the place's local time and zone.
func (b *ircNetwork) handleTimePlaceCommand(target, sender, query string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	label, zone, err := resolveTimePlace(ctx, query)
	var loc *time.Location
	if err == nil {
		loc, err = time.LoadLocation(zone)
	}
	switch {
	case errors.Is(err, errTimeNoPlace):
		b.sendPrivmsg(target, b.sanitize(fmt.Sprintf("@%s: Time: no place found for %q", sender, query)))
		return
	case err != nil:
		b.sendPrivmsg(target, b.sanitize(fmt.Sprintf("@%s: Time: lookup failed: %v", sender, err)))
		return
	}
	now := time.Now().In(loc)
	// bold place · bold yellow clock · light-cyan zone (same palette as !weather)
	b.sendPrivmsg(target, b.sanitize(fmt.Sprintf("\x0303,01[TIME]\x03 🕒 %s · %s%s%s%s · %s%s (UTC%s)%s",
		ircBoldLabel(label), ircBold, ircYellow, now.Format("Mon 2 Jan 15:04:05 MST 2006"), ircEnd+ircBold,
		ircLightCyan, zone, now.Format("-07:00"), ircEnd)))
}
