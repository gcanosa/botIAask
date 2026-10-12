package irc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestResolveIPCheckRejects(t *testing.T) {
	for _, q := range []string{"10.0.0.1", "127.0.0.1", "192.168.1.1", "::1", "localhost", "bad host;rm", ""} {
		if _, _, err := resolveIPCheck(context.Background(), q); err == nil {
			t.Errorf("%q: want error", q)
		}
	}
	if ip, _, err := resolveIPCheck(context.Background(), "8.8.8.8"); err != nil || ip.String() != "8.8.8.8" {
		t.Errorf("8.8.8.8: got %v, %v", ip, err)
	}
}

func TestFormatIPCheck(t *testing.T) {
	r := &ipwhoResp{IP: "8.8.8.8", City: "San Jose", Region: "California", Country: "United States",
		CountryCode: "US", Continent: "North America", Latitude: 37.3394, Longitude: -121.895}
	r.Connection.ASN, r.Connection.Org, r.Connection.ISP = 15169, "Google LLC", "Google LLC"
	r.Timezone.ID, r.Timezone.UTC = "America/Los_Angeles", "-07:00"
	got := formatIPCheck("dns.google", 1, "dns.google", r)
	for _, want := range []string{"dns.google → 8.8.8.8 (+1 more)", "San Jose, California, United States [US] · North America",
		"37.3394, -121.8950", "AS15169 Google LLC", "America/Los_Angeles (UTC-07:00)"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "ISP") || strings.Contains(got, "(dns.google)") {
		t.Errorf("redundant ISP/rdns in %q", got)
	}
}

func TestRDAPSummary(t *testing.T) {
	var r rdapResp
	err := json.Unmarshal([]byte(`{"events":[{"eventAction":"registration","eventDate":"1997-10-02T01:29:26Z"},{"eventAction":"expiration","eventDate":"2027-07-31T14:55:32Z"}],
	"entities":[{"roles":["registrar"],"vcardArray":["vcard",[["version",{},"text","4.0"],["fn",{},"text","get.gov"]]]},
	{"roles":["registrant"],"vcardArray":["vcard",[["fn",{},"text","REDACTED FOR PRIVACY"],["org",{},"text","NASA"],["adr",{},"text",["","",""]]]]},
	{"roles":["technical"],"vcardArray":["vcard",[["fn",{},"text","Someone"]]]}]}`), &r)
	if err != nil {
		t.Fatal(err)
	}
	o, reg, c, e := rdapSummary(&r)
	if o != "NASA" || reg != "get.gov" || c != "1997-10-02" || e != "2027-07-31" {
		t.Errorf("got %q %q %q %q", o, reg, c, e)
	}
	r.Entities[1].Vcard = r.Entities[1].Vcard[:1] // registrant without properties
	if o, _, _, _ := rdapSummary(&r); o != "" {
		t.Errorf("owner = %q, want empty", o)
	}
	if got := formatNS([]string{"a", "b", "c", "d", "e"}, 3); got != "a, b, c (+2)" {
		t.Errorf("formatNS = %q", got)
	}
}
