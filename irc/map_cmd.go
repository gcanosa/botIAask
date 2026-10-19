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

	"botIAask/internal/httpx"
	"botIAask/internal/ttlcache"
	"botIAask/meta"
)

// mapAPI is OpenStreetMap Nominatim (var so tests can point it at an httptest server). No key; the
// usage policy asks for a real User-Agent and at most 1 request per second.
var mapAPI = "https://nominatim.openstreetmap.org"

var (
	mapCache   = ttlcache.Cache[string, string]{TTL: 24 * time.Hour, Max: 512}
	mapMu      sync.Mutex
	mapLast    time.Time
	errNoPlace = errors.New("place not found")
)

func googleMapsURL(query string) string {
	return "https://www.google.com/maps/search/?api=1&query=" + url.QueryEscape(query)
}

// googleMapsAt centers the map without a pin, zoomed to fit the place: a pin search zooms to
// street level, which shows a country as one random field.
func googleMapsAt(lat, lon string, zoom int) string {
	return fmt.Sprintf("https://www.google.com/maps/@?api=1&map_action=map&center=%s,%s&zoom=%d", lat, lon, zoom)
}

// mapZoom picks a Google Maps zoom level from Nominatim's addresstype, falling back to place_rank
// (4 country … 30 building). addresstype comes first: capitals are admin areas, so Bucharest ranks 8
// (region level) while still being a "city".
func mapZoom(addressType string, rank int) int {
	switch addressType {
	case "country":
		return 5
	case "state", "region", "province":
		return 7
	case "county", "state_district":
		return 9
	case "city":
		return 12
	case "town", "municipality":
		return 13
	case "village", "suburb", "borough", "city_district":
		return 14
	}
	switch {
	case rank <= 4:
		return 5
	case rank <= 8:
		return 7
	case rank <= 12:
		return 9
	case rank <= 16:
		return 12
	case rank <= 20:
		return 14
	}
	return 16
}

// geocode returns Nominatim's best match. ok=false means the 1 req/s budget is spent (no request made).
func geocode(ctx context.Context, place string) (name, lat, lon string, zoom int, ok bool, err error) {
	mapMu.Lock()
	if time.Since(mapLast) < time.Second {
		mapMu.Unlock()
		return "", "", "", 0, false, nil
	}
	mapLast = time.Now()
	mapMu.Unlock()

	v := url.Values{"q": {place}, "format": {"jsonv2"}, "limit": {"1"}, "accept-language": {"en"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mapAPI+"/search?"+v.Encode(), nil)
	if err != nil {
		return "", "", "", 0, true, err
	}
	req.Header.Set("User-Agent", meta.Name+"/"+meta.Version+" (IRC bot)")
	resp, err := latestHTTP.Do(req)
	if err != nil {
		return "", "", "", 0, true, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", "", 0, true, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var r []struct {
		DisplayName string `json:"display_name"`
		Lat         string `json:"lat"`
		Lon         string `json:"lon"`
		PlaceRank   int    `json:"place_rank"`
		AddressType string `json:"addresstype"`
	}
	if err := httpx.DecodeJSON(resp.Body, httpx.MaxBody, &r); err != nil {
		return "", "", "", 0, true, err
	}
	if len(r) == 0 {
		return "", "", "", 0, true, errNoPlace
	}
	return r[0].DisplayName, r[0].Lat, r[0].Lon, mapZoom(r[0].AddressType, r[0].PlaceRank), true, nil
}

// handleMapCommand geocodes the place and links Google Maps centered on it. If Nominatim is busy
// or down it still answers with a plain search link (Google resolves the text itself).
func (b *ircNetwork) handleMapCommand(target, sender, place string) {
	key := strings.ToLower(place)
	if line, ok := mapCache.Get(key); ok {
		b.sendPrivmsg(target, b.sanitize(line))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	name, lat, lon, zoom, ok, err := geocode(ctx, place)
	if errors.Is(err, errNoPlace) {
		b.sendPrivmsg(target, b.sanitize(fmt.Sprintf("@%s: map: no place found for %q", sender, place)))
		return
	}
	if !ok || err != nil {
		b.sendPrivmsg(target, b.sanitize("\x0303,01[MAP]\x03 \x02"+place+"\x02 · "+googleMapsURL(place)))
		return
	}
	line := fmt.Sprintf("\x0303,01[MAP]\x03 \x02%s\x02 (%s, %s) · %s", cutRunes(name, 120), lat, lon, googleMapsAt(lat, lon, zoom))
	mapCache.Put(key, line)
	b.sendPrivmsg(target, b.sanitize(line))
}
