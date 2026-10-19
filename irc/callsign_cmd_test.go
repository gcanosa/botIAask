package irc

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"botIAask/config"
)

// HamQTH: login once, re-login when the session expires; not found there → callook.info (US);
// invalid input never hits the network.
func TestCallsignCommand(t *testing.T) {
	logins := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case r.URL.Path == "/W1AW/json":
			w.Write([]byte(`{"status":"VALID","type":"CLUB","current":{"callsign":"W1AW"},"trustee":{"callsign":"AF5ID"},
				"name":"ARRL HQ OPERATORS CLUB","address":{"line2":"NEWINGTON, CT 06111"},"location":{"gridsquare":"FN31pr"},
				"otherInfo":{"grantDate":"12/08/2020","expiryDate":"02/26/2031"}}`))
		case r.URL.Path != "/xml.php":
			w.Write([]byte(`{"status":"INVALID"}`))
		case q.Get("u") != "":
			logins++
			w.Write([]byte(`<HamQTH><session><session_id>s` + string(rune('0'+logins)) + `</session_id></session></HamQTH>`))
		case q.Get("id") == "s1" && q.Get("callsign") == "LU1AA":
			w.Write([]byte(`<HamQTH><session><error>Session does not exist or expired</error></session></HamQTH>`))
		case q.Get("callsign") == "LU1AA":
			w.Write([]byte(`<HamQTH><search><callsign>lu1aa</callsign><adr_name>Juan Perez</adr_name><qth>Buenos Aires; Argentina</qth>
				<country>Argentina</country><grid>GF05</grid><cq>13</cq><itu>14</itu><lotw>Y</lotw></search></HamQTH>`))
		default:
			w.Write([]byte(`<HamQTH><session><error>Callsign not found</error></session></HamQTH>`))
		}
	}))
	defer srv.Close()
	oldH, oldC := hamqthAPI, callookAPI
	hamqthAPI, callookAPI = srv.URL+"/xml.php", srv.URL
	defer func() { hamqthAPI, callookAPI = oldH, oldC }()
	hamqthSession = ""

	n, out := newCaptureNetwork(t, &config.Config{HamQTH: config.HamQTHConfig{Username: "u", Password: "p"}})
	n.handleCallsignCommand("#c", "alice", "lu1aa")
	n.handleCallsignCommand("#c", "alice", "W1AW")
	n.handleCallsignCommand("#c", "alice", "ZZ9ZZZ")
	n.handleCallsignCommand("#c", "alice", "not a call!")
	got := out.all()
	want := []string{
		"\x02LU1AA\x02 · Juan Perez · Buenos Aires; Argentina · grid GF05 · CQ 13 ITU 14 · QSL: LoTW · https://www.qrz.com/db/LU1AA",
		"\x02W1AW\x02 · ARRL HQ OPERATORS CLUB · club, trustee AF5ID · NEWINGTON, CT 06111, USA · grid FN31pr",
		"ZZ9ZZZ not found",
		"not a valid callsign",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d lines: %q", len(got), got)
	}
	for i, w := range want {
		if !strings.Contains(got[i], w) {
			t.Errorf("line %d: missing %q in %q", i, w, got[i])
		}
	}
	if logins != 2 {
		t.Errorf("logins = %d, want 2 (initial + after expired session)", logins)
	}
}
