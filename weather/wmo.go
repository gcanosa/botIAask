package weather

// WMOCodeSummary returns a short human label for a WMO weather code (0–99).
func WMOCodeSummary(code int) string {
	switch {
	case code == 0:
		return "Clear"
	case code == 1, code == 2:
		return "Partly cloudy"
	case code == 3:
		return "Overcast"
	case code >= 45 && code <= 48:
		return "Fog"
	case code >= 51 && code <= 57:
		return "Drizzle"
	case code >= 61 && code <= 67:
		return "Rain"
	case code >= 71 && code <= 77:
		return "Snow"
	case code >= 80 && code <= 82:
		return "Showers"
	case code >= 85 && code <= 86:
		return "Snow showers"
	case code >= 95 && code <= 99:
		return "Storm"
	default:
		return "Mixed"
	}
}

// IconKind groups codes for client SVG selection (theme-colored icons).
func IconKind(code int) string {
	switch {
	case code == 0:
		return "clear"
	case code == 1, code == 2:
		return "partly"
	case code == 3:
		return "cloud"
	case code >= 45 && code <= 48:
		return "fog"
	case code >= 51 && code <= 67:
		return "rain"
	case code >= 71 && code <= 77, code >= 85 && code <= 86:
		return "snow"
	case code >= 80 && code <= 82:
		return "shower"
	case code >= 95 && code <= 99:
		return "thunder"
	default:
		return "cloud"
	}
}
