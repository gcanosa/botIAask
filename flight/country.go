package flight

import "strings"

// countryName turns ISO 3166-1 alpha-2 into a long name for IRC display. Unknown codes are returned as-is.
func countryName(iso2 string) string {
	k := strings.ToUpper(strings.TrimSpace(iso2))
	if len(k) != 2 {
		return iso2
	}
	if n, ok := iso2CountryName[k]; ok {
		return n
	}
	return k
}

// iso2CountryName common destinations (extend as needed).
var iso2CountryName = map[string]string{
	"AD": "Andorra", "AE": "United Arab Emirates", "AF": "Afghanistan", "AG": "Antigua and Barbuda",
	"AR": "Argentina", "AT": "Austria", "AU": "Australia", "BE": "Belgium", "BG": "Bulgaria",
	"BO": "Bolivia", "BR": "Brazil", "BS": "Bahamas", "CA": "Canada", "CH": "Switzerland",
	"CL": "Chile", "CN": "China", "CO": "Colombia", "CR": "Costa Rica", "CU": "Cuba",
	"CY": "Cyprus", "CZ": "Czechia", "DE": "Germany", "DK": "Denmark", "DO": "Dominican Republic",
	"DZ": "Algeria", "EC": "Ecuador", "EE": "Estonia", "EG": "Egypt", "ES": "Spain", "FI": "Finland",
	"FR": "France", "GB": "United Kingdom", "GR": "Greece", "GT": "Guatemala", "HK": "Hong Kong",
	"HR": "Croatia", "HU": "Hungary", "ID": "Indonesia", "IE": "Ireland", "IL": "Israel", "IN": "India",
	"IR": "Iran", "IS": "Iceland", "IT": "Italy", "JM": "Jamaica", "JO": "Jordan", "JP": "Japan",
	"KE": "Kenya", "KR": "South Korea", "KW": "Kuwait", "KZ": "Kazakhstan", "LB": "Lebanon", "LT": "Lithuania",
	"LU": "Luxembourg", "LV": "Latvia", "MA": "Morocco", "MC": "Monaco", "MX": "Mexico", "MY": "Malaysia",
	"NG": "Nigeria", "NI": "Nicaragua", "NL": "Netherlands", "NO": "Norway", "NP": "Nepal", "NZ": "New Zealand",
	"OM": "Oman", "PA": "Panama", "PE": "Peru", "PH": "Philippines", "PK": "Pakistan", "PL": "Poland", "PT": "Portugal",
	"PY": "Paraguay", "QA": "Qatar", "RO": "Romania", "RU": "Russia", "SA": "Saudi Arabia", "SE": "Sweden", "SG": "Singapore",
	"SI": "Slovenia", "SK": "Slovakia", "SV": "El Salvador", "TH": "Thailand", "TR": "Türkiye", "TW": "Taiwan", "UA": "Ukraine",
	"US": "United States", "UY": "Uruguay", "UZ": "Uzbekistan", "VE": "Venezuela", "VN": "Vietnam", "ZA": "South Africa",
}
