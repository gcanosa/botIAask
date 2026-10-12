package irc

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"
)

var ipcheckHTTP = &http.Client{Timeout: 10 * time.Second}

// ipwhoURL is the ipwho.is endpoint (HTTPS, no API key; free tier is rate-limited).
var ipwhoURL = "https://ipwho.is/"

type ipwhoResp struct {
	Success     bool    `json:"success"`
	Message     string  `json:"message"`
	IP          string  `json:"ip"`
	Type        string  `json:"type"`
	Continent   string  `json:"continent"`
	Country     string  `json:"country"`
	CountryCode string  `json:"country_code"`
	Region      string  `json:"region"`
	City        string  `json:"city"`
	Postal      string  `json:"postal"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	Connection  struct {
		ASN    int    `json:"asn"`
		Org    string `json:"org"`
		ISP    string `json:"isp"`
		Domain string `json:"domain"`
	} `json:"connection"`
	Timezone struct {
		ID  string `json:"id"`
		UTC string `json:"utc"`
	} `json:"timezone"`
}

// publicIP reports whether ip is worth geolocating (not private, loopback, link-local, etc.).
func publicIP(ip net.IP) bool {
	return ip.IsGlobalUnicast() && !ip.IsPrivate()
}

// resolveIPCheck turns the query into one public IP (IPv4 preferred) plus the count of
// other resolved addresses.
func resolveIPCheck(ctx context.Context, query string) (net.IP, int, error) {
	if ip := net.ParseIP(query); ip != nil {
		if !publicIP(ip) {
			return nil, 0, fmt.Errorf("%s is not a public address", query)
		}
		return ip, 0, nil
	}
	if !validPingHost(query) || !strings.Contains(query, ".") {
		return nil, 0, fmt.Errorf("invalid IP or hostname")
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, query)
	if err != nil || len(addrs) == 0 {
		return nil, 0, fmt.Errorf("cannot resolve %s", query)
	}
	var pick net.IP
	for _, a := range addrs {
		if !publicIP(a.IP) {
			continue
		}
		if pick == nil || (pick.To4() == nil && a.IP.To4() != nil) {
			pick = a.IP
		}
	}
	if pick == nil {
		// ponytail: don't echo internal addresses into a public channel.
		return nil, 0, fmt.Errorf("%s has no public address", query)
	}
	return pick, len(addrs) - 1, nil
}

func fetchIPWho(ctx context.Context, ip net.IP) (*ipwhoResp, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ipwhoURL+url.PathEscape(ip.String()), nil)
	if err != nil {
		return nil, err
	}
	resp, err := ipcheckHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("lookup service unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lookup service returned HTTP %d", resp.StatusCode)
	}
	var r ipwhoResp
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 64<<10)).Decode(&r); err != nil {
		return nil, fmt.Errorf("bad response from lookup service")
	}
	if !r.Success {
		msg := r.Message
		if msg == "" {
			msg = "no data"
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return &r, nil
}

// formatIPCheck renders one IRC line. query is what the user typed, rdns the PTR name (may be "").
func formatIPCheck(query string, more int, rdns string, r *ipwhoResp) string {
	head := query
	if query != r.IP {
		head += " → " + r.IP
	}
	if more > 0 {
		head += fmt.Sprintf(" (+%d more)", more)
	}
	if rdns != "" && !strings.EqualFold(rdns, query) {
		head += " (" + rdns + ")"
	}

	var place []string
	for _, s := range []string{r.City, r.Region, r.Country} {
		if s != "" && (len(place) == 0 || place[len(place)-1] != s) {
			place = append(place, s)
		}
	}
	loc := strings.Join(place, ", ")
	if r.CountryCode != "" {
		loc += " [" + r.CountryCode + "]"
	}
	if r.Continent != "" {
		loc += " · " + r.Continent
	}

	parts := []string{"\x0303,01[IPCHECK]\x03 " + head}
	if loc = strings.TrimPrefix(loc, " · "); loc != "" {
		parts = append(parts, loc)
	}
	parts = append(parts, fmt.Sprintf("%s %.4f, %.4f", ircBoldLabel("lat/lon"), r.Latitude, r.Longitude))
	if c := r.Connection; c.ASN != 0 || c.Org != "" {
		n := c.Org
		if c.ASN != 0 {
			n = fmt.Sprintf("AS%d %s", c.ASN, c.Org)
		}
		if c.ISP != "" && c.ISP != c.Org {
			n += " (ISP " + c.ISP + ")"
		}
		if c.Domain != "" {
			n += " " + c.Domain
		}
		parts = append(parts, ircBoldLabel("net")+" "+strings.TrimSpace(n))
	}
	if r.Timezone.ID != "" {
		parts = append(parts, fmt.Sprintf("%s %s (UTC%s)", ircBoldLabel("tz"), r.Timezone.ID, r.Timezone.UTC))
	}
	if r.Postal != "" {
		parts = append(parts, ircBoldLabel("postal")+" "+r.Postal)
	}
	return strings.Join(parts, " · ")
}

// handleIPCheckCommand geolocates an IP or hostname (!ipcheck / !hostcheck).
func (b *ircNetwork) handleIPCheckCommand(target, sender, query string) {
	query = strings.Trim(strings.TrimSpace(query), "[]")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	ip, more, err := resolveIPCheck(ctx, query)
	if err == nil {
		var r *ipwhoResp
		if r, err = fetchIPWho(ctx, ip); err == nil {
			var rdns string
			if names, _ := net.DefaultResolver.LookupAddr(ctx, ip.String()); len(names) > 0 {
				rdns = strings.TrimSuffix(names[0], ".")
			}
			b.sendPrivmsg(target, b.sanitize(formatIPCheck(query, more, rdns, r)))
			if net.ParseIP(query) == nil {
				if line := domainInfo(ctx, query); line != "" {
					b.sendPrivmsg(target, b.sanitize(line))
				}
			}
			return
		}
	}
	b.sendPrivmsg(target, b.sanitize(fmt.Sprintf("@%s: IP check: %v", sender, err)))
}

// rdapURL is the RDAP bootstrap redirector (free, no key); it 302s to the TLD's registry.
var rdapURL = "https://rdap.org/domain/"

type rdapResp struct {
	Events []struct {
		Action string `json:"eventAction"`
		Date   string `json:"eventDate"`
	} `json:"events"`
	Entities []struct {
		Roles []string          `json:"roles"`
		Vcard []json.RawMessage `json:"vcardArray"`
	} `json:"entities"`
}

// vcardField returns the first string value of a jCard property (e.g. "fn", "org").
func vcardField(vcard []json.RawMessage, name string) string {
	if len(vcard) < 2 {
		return ""
	}
	var props [][]any
	if json.Unmarshal(vcard[1], &props) != nil {
		return ""
	}
	for _, p := range props {
		if len(p) >= 4 && p[0] == name {
			if v, ok := p[3].(string); ok {
				return strings.TrimSpace(v)
			}
		}
	}
	return ""
}

// redacted reports whether a WHOIS/RDAP value is a GDPR/privacy placeholder.
func redacted(s string) bool {
	l := strings.ToLower(s)
	for _, w := range []string{"redacted", "privacy", "not disclosed", "withheld", "data protected", "proxy"} {
		if strings.Contains(l, w) {
			return true
		}
	}
	return false
}

// rdapSummary extracts owner org (unless redacted), registrar and reg/exp dates.
func rdapSummary(r *rdapResp) (owner, registrar, created, expires string) {
	for _, e := range r.Entities {
		switch {
		case slices.Contains(e.Roles, "registrant"):
			for _, f := range []string{"org", "fn"} {
				if v := vcardField(e.Vcard, f); v != "" && !redacted(v) {
					owner = v
					break
				}
			}
		case slices.Contains(e.Roles, "registrar"):
			registrar = vcardField(e.Vcard, "fn")
		}
	}
	for _, ev := range r.Events {
		d, _, _ := strings.Cut(ev.Date, "T")
		switch ev.Action {
		case "registration":
			created = d
		case "expiration":
			expires = d
		}
	}
	return
}

func fetchRDAP(ctx context.Context, domain string) (*rdapResp, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rdapURL+url.PathEscape(domain), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/rdap+json")
	resp, err := ipcheckHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rdap: HTTP %d", resp.StatusCode)
	}
	var r rdapResp
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 512<<10)).Decode(&r); err != nil {
		return nil, err
	}
	return &r, nil
}

// domainInfo is the [WHOIS] line for a hostname: owner org, registrar, dates (RDAP) and
// nameservers (live DNS). Any part that fails is just left out; "" when nothing is known.
func domainInfo(ctx context.Context, host string) string {
	domain, err := publicsuffix.EffectiveTLDPlusOne(strings.ToLower(strings.TrimSuffix(host, ".")))
	if err != nil {
		return ""
	}
	parts := []string{"\x0303,01[WHOIS]\x03 " + domain}
	if r, err := fetchRDAP(ctx, domain); err == nil {
		owner, registrar, created, expires := rdapSummary(r)
		for _, kv := range [][2]string{{"owner", owner}, {"registrar", registrar}, {"reg", created}, {"exp", expires}} {
			if kv[1] != "" {
				parts = append(parts, ircBoldLabel(kv[0])+" "+kv[1])
			}
		}
	}
	if nss, err := net.DefaultResolver.LookupNS(ctx, domain); err == nil && len(nss) > 0 {
		var names []string
		for _, ns := range nss {
			names = append(names, strings.ToLower(strings.TrimSuffix(ns.Host, ".")))
		}
		slices.Sort(names)
		parts = append(parts, ircBoldLabel("NS")+" "+formatNS(names, 3))
	}
	if len(parts) == 1 {
		return ""
	}
	return strings.Join(parts, " · ")
}

// formatNS lists up to n nameservers, then "(+k)" for the rest.
func formatNS(names []string, n int) string {
	s := strings.Join(names[:min(n, len(names))], ", ")
	if len(names) > n {
		s += fmt.Sprintf(" (+%d)", len(names)-n)
	}
	return s
}
