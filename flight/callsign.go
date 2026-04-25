package flight

import (
	"sort"
	"strings"
)

// iata2icao only supports parseIATAStyleFlight for carriers whose IATA code contains a digit
// (U2, 6E, …) so we can split the string into airline + number before forming flight_iata.
var iata2icao = map[string]string{
	"AA": "AAL", "DL": "DAL", "UA": "UAL", "WN": "SWA", "AS": "ASA", "B6": "JBU", "F9": "FFT",
	"HA": "HAL", "NK": "NKS", "SY": "SCX", "G4": "AAY", "XP": "VXP", "MX": "MXY", "UP": "BHS",
	"AC": "ACA", "WS": "WJA", "PD": "POE", "TS": "TSC", "QK": "JZA", "Y9": "DAT",
	"BA": "BAW", "U2": "EZY", "FR": "RYR", "W6": "WZZ", "EW": "EWG", "SN": "BEL", "4U": "GWI",
	"IB": "IBE", "VY": "VLG", "TO": "TVF", "AY": "FIN", "SK": "SAS", "DY": "NAX", "WF": "WIF",
	"LH": "DLH", "LX": "SWR", "OS": "AUA", "DE": "CFG",
	"AF": "AFR", "KL": "KLM", "HV": "TRA", "TP": "TAP", "EI": "EIN", "LO": "LOT", "OK": "CSA",
	"EY": "ETD", "QR": "QTR", "EK": "UAE", "FZ": "FDB", "SV": "SVA", "MS": "MSR", "AT": "RAM",
	"ET": "ETH", "SA": "SAA", "KQ": "KQA", "J2": "AHY", "HY": "UZB",
	"NH": "ANA", "JL": "JAL", "TR": "TGW", "SQ": "SIA", "MI": "MMA", "CX": "CPA", "KA": "HDA",
	"KE": "KAL", "OZ": "AAR", "TG": "THA", "VJ": "VJC", "5J": "CEB", "PR": "PAL",
	"AI": "AIC", "6E": "IGO", "SG": "SEJ", "IX": "AXB", "G8": "GOW", "UK": "VTI", "9W": "JAI",
	"CA": "CCA", "MU": "CES", "CZ": "CSN", "3U": "CSC", "HO": "DKH", "9C": "CQH", "GS": "GCR",
	"QF": "QFA", "JQ": "JST", "VA": "VOZ", "NZ": "ANZ", "AR": "ARG", "LA": "LAN", "AV": "AVA",
	"G3": "GLO", "AD": "AZU", "CM": "CMP", "AM": "AMX",
}

// parseIATAStyleFlight splits e.g. "AA100" → ("AA", "100"), "U2800" → ("U2", "800"), "AAY12" → ("AAY", "12").
func parseIATAStyleFlight(s string) (iata, num string, ok bool) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if len(s) < 3 {
		return "", "", false
	}
	isAllLetters := func(t string) bool {
		for _, c := range t {
			if c < 'A' || c > 'Z' {
				return false
			}
		}
		return len(t) > 0
	}
	isAllDigits := func(t string) bool {
		for _, c := range t {
			if c < '0' || c > '9' {
				return false
			}
		}
		return len(t) > 0
	}
	tryLetterPrefix := func(prefixLen int) (string, string, bool) {
		if len(s) <= prefixLen {
			return "", "", false
		}
		p := s[:prefixLen]
		n := s[prefixLen:]
		if !isAllLetters(p) || !isAllDigits(n) || len(n) < 1 || len(n) > 4 {
			return "", "", false
		}
		return p, n, true
	}
	keys := make([]string, 0, len(iata2icao))
	for k := range iata2icao {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		li, lj := len(keys[i]), len(keys[j])
		if li != lj {
			return li > lj
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys {
		if len(s) > len(k) && strings.HasPrefix(s, k) {
			n := s[len(k):]
			if isAllDigits(n) && len(n) >= 1 && len(n) <= 4 {
				return k, n, true
			}
		}
	}
	if a, n, y := tryLetterPrefix(3); y {
		return a, n, true
	}
	if a, n, y := tryLetterPrefix(2); y {
		return a, n, true
	}
	return "", "", false
}

// flightIATAString returns AirLabs flight_iata (e.g. AA100) or "" if the token is invalid.
func flightIATAString(token string) string {
	a, n, ok := parseIATAStyleFlight(token)
	if !ok {
		return ""
	}
	return a + n
}
