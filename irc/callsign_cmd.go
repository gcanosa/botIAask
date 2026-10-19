package irc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"botIAask/internal/httpx"
	"botIAask/internal/ttlcache"
	"botIAask/meta"
)

// Vars so tests can point them at an httptest server.
var (
	hamqthAPI  = "https://www.hamqth.com/xml.php"
	callookAPI = "https://callook.info"
)

var (
	callsignRe      = regexp.MustCompile(`^[A-Z0-9]{1,4}/?[A-Z0-9]{1,8}(/[A-Z0-9]{1,4})?$`)
	callsignCache   = ttlcache.Cache[string, string]{TTL: 6 * time.Hour, Max: 512}
	errCallNotFound = errors.New("callsign not found")

	// HamQTH sessions last 1h; reuse one instead of logging in per lookup.
	hamqthMu      sync.Mutex
	hamqthSession string
	hamqthExpires time.Time
)

type hamqthResp struct {
	Session struct {
		ID    string `xml:"session_id"`
		Error string `xml:"error"`
	} `xml:"session"`
	Search struct {
		Callsign   string `xml:"callsign"`
		Nick       string `xml:"nick"`
		Name       string `xml:"adr_name"`
		QTH        string `xml:"qth"`
		City       string `xml:"adr_city"`
		Country    string `xml:"country"`
		Grid       string `xml:"grid"`
		CQ         string `xml:"cq"`
		ITU        string `xml:"itu"`
		LicYear    string `xml:"lic_year"`
		LoTW       string `xml:"lotw"`
		EQSL       string `xml:"eqsl"`
		QSLVia     string `xml:"qsl_via"`
		USState    string `xml:"us_state"`
		AdrCountry string `xml:"adr_country"`
	} `xml:"search"`
}

// getXML fetches and decodes. Errors never include the URL: HamQTH login puts the password in the query.
func getXML(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return errors.New("bad request")
	}
	req.Header.Set("User-Agent", meta.Name+"/"+meta.Version+" (IRC bot)")
	resp, err := latestHTTP.Do(req)
	if err != nil {
		return errors.New("lookup service unreachable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return httpx.DecodeXML(resp.Body, httpx.MaxBody, v)
}

func hamqthLogin(ctx context.Context, user, pass string) (string, error) {
	var r hamqthResp
	if err := getXML(ctx, hamqthAPI+"?"+url.Values{"u": {user}, "p": {pass}}.Encode(), &r); err != nil {
		return "", err
	}
	if r.Session.ID == "" {
		return "", fmt.Errorf("HamQTH login failed: %s", r.Session.Error)
	}
	return r.Session.ID, nil
}

// hamqthLookup searches one callsign, logging in (again) when there is no live session.
func hamqthLookup(ctx context.Context, user, pass, call string) (string, error) {
	for attempt := 0; attempt < 2; attempt++ {
		hamqthMu.Lock()
		if hamqthSession == "" || time.Now().After(hamqthExpires) {
			id, err := hamqthLogin(ctx, user, pass)
			if err != nil {
				hamqthMu.Unlock()
				return "", err
			}
			hamqthSession, hamqthExpires = id, time.Now().Add(55*time.Minute)
		}
		id := hamqthSession
		hamqthMu.Unlock()

		var r hamqthResp
		v := url.Values{"id": {id}, "callsign": {call}, "prg": {meta.Name}}
		if err := getXML(ctx, hamqthAPI+"?"+v.Encode(), &r); err != nil {
			return "", err
		}
		switch e := r.Session.Error; {
		case strings.Contains(strings.ToLower(e), "session"):
			hamqthMu.Lock()
			hamqthSession = ""
			hamqthMu.Unlock()
			continue
		case strings.Contains(strings.ToLower(e), "not found"):
			return "", errCallNotFound
		case e != "":
			return "", errors.New("HamQTH: " + e)
		}
		return formatHamQTH(r), nil
	}
	return "", errors.New("HamQTH session expired")
}

func formatHamQTH(r hamqthResp) string {
	s := r.Search
	parts := []string{"\x02" + strings.ToUpper(s.Callsign) + "\x02"}
	name := firstNonEmpty(s.Name, s.Nick)
	if name != "" {
		parts = append(parts, name)
	}
	loc := firstNonEmpty(s.QTH, s.City)
	if s.USState != "" {
		loc = strings.TrimSpace(loc + ", " + s.USState)
	}
	// Users often type the country into their QTH already ("Bucuresti; Romania").
	if c := firstNonEmpty(s.Country, s.AdrCountry); c != "" && !strings.Contains(strings.ToLower(loc), strings.ToLower(c)) {
		loc = strings.TrimPrefix(loc+", "+c, ", ")
	}
	if loc != "" {
		parts = append(parts, loc)
	}
	if s.Grid != "" {
		parts = append(parts, "grid "+s.Grid)
	}
	if s.CQ != "" || s.ITU != "" {
		parts = append(parts, fmt.Sprintf("CQ %s ITU %s", s.CQ, s.ITU))
	}
	if s.LicYear != "" {
		parts = append(parts, "licensed "+s.LicYear)
	}
	var qsl []string
	if s.LoTW == "Y" {
		qsl = append(qsl, "LoTW")
	}
	if s.EQSL == "Y" {
		qsl = append(qsl, "eQSL")
	}
	if s.QSLVia != "" {
		qsl = append(qsl, "via "+s.QSLVia)
	}
	if len(qsl) > 0 {
		parts = append(parts, "QSL: "+strings.Join(qsl, ", "))
	}
	return strings.Join(parts, " · ")
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// callookLookup covers US (FCC) licences without an account.
func callookLookup(ctx context.Context, call string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, callookAPI+"/"+url.PathEscape(call)+"/json", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", meta.Name+"/"+meta.Version+" (IRC bot)")
	resp, err := latestHTTP.Do(req)
	if err != nil {
		return "", errors.New("lookup service unreachable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var r struct {
		Status  string `json:"status"`
		Type    string `json:"type"`
		Current struct {
			Callsign  string `json:"callsign"`
			OperClass string `json:"operClass"`
		} `json:"current"`
		Trustee struct {
			Callsign string `json:"callsign"`
		} `json:"trustee"`
		Name    string `json:"name"`
		Address struct {
			Line2 string `json:"line2"`
		} `json:"address"`
		Location struct {
			Grid string `json:"gridsquare"`
		} `json:"location"`
		OtherInfo struct {
			GrantDate  string `json:"grantDate"`
			ExpiryDate string `json:"expiryDate"`
		} `json:"otherInfo"`
	}
	if err := httpx.DecodeJSON(resp.Body, httpx.MaxBody, &r); err != nil {
		return "", err
	}
	if r.Status != "VALID" {
		return "", errCallNotFound
	}
	parts := []string{"\x02" + r.Current.Callsign + "\x02", r.Name}
	if r.Type == "CLUB" && r.Trustee.Callsign != "" {
		parts = append(parts, "club, trustee "+r.Trustee.Callsign)
	} else if r.Current.OperClass != "" {
		parts = append(parts, strings.ToLower(r.Current.OperClass)+" class")
	}
	if r.Address.Line2 != "" {
		parts = append(parts, r.Address.Line2+", USA")
	}
	if r.Location.Grid != "" {
		parts = append(parts, "grid "+r.Location.Grid)
	}
	if r.OtherInfo.ExpiryDate != "" {
		parts = append(parts, "granted "+r.OtherInfo.GrantDate+", expires "+r.OtherInfo.ExpiryDate)
	}
	return strings.Join(parts, " · "), nil
}

// handleCallsignCommand looks up a ham radio callsign (!callsign / !qrz <call>): HamQTH when an
// account is configured, then callook.info (US only). Reply ends with the QRZ.com page.
func (b *ircNetwork) handleCallsignCommand(target, sender, call string) {
	call = strings.ToUpper(call)
	if !callsignRe.MatchString(call) {
		b.sendPrivmsg(target, b.sanitize(fmt.Sprintf("@%s: callsign: %q is not a valid callsign", sender, call)))
		return
	}
	if line, ok := callsignCache.Get(call); ok {
		b.sendPrivmsg(target, b.sanitize(line))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	hq := b.getCfg().HamQTH
	body, err := "", errCallNotFound
	if hq.Username != "" && hq.Password != "" {
		body, err = hamqthLookup(ctx, hq.Username, hq.Password, call)
	}
	if errors.Is(err, errCallNotFound) {
		body, err = callookLookup(ctx, call)
	}
	link := "https://www.qrz.com/db/" + url.PathEscape(call)
	if err != nil {
		if errors.Is(err, errCallNotFound) {
			err = fmt.Errorf("%s not found (%s)", call, link)
		}
		b.sendPrivmsg(target, b.sanitize(fmt.Sprintf("@%s: callsign: %v", sender, err)))
		return
	}
	line := "\x0303,01[CALLSIGN]\x03 " + body + " · " + link
	callsignCache.Put(call, line)
	b.sendPrivmsg(target, b.sanitize(line))
}
