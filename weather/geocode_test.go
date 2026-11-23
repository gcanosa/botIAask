package weather

import "testing"

func TestPreferHint(t *testing.T) {
	res := []geoPlace{
		{Name: "Paris", Country: "France", CountryCode: "FR", Admin1: "Île-de-France Region"},
		{Name: "Paris", Country: "United States", CountryCode: "US", Admin1: "Texas"},
		{Name: "Paris", Country: "Czechia", CountryCode: "CZ", Admin1: "Vysočina"},
	}
	for hint, want := range map[string]string{
		" Texas":            "US",
		"czech republic":    "CZ", // alias: the API says "Czechia"
		"usa":               "US",
		"CZ":                "CZ",
		"somewhere, france": "FR", // any comma part may match
		"Narnia":            "FR", // no match keeps the API's first result
	} {
		c := append([]geoPlace(nil), res...)
		preferHint(c, hint)
		if c[0].CountryCode != want {
			t.Errorf("hint %q: got %s, want %s", hint, c[0].CountryCode, want)
		}
	}
}
