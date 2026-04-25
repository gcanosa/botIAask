package flight

import (
	"fmt"
	"strings"
	"time"
)

// PhaseLabel maps API status to a short user-facing label.
func PhaseLabel(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "scheduled":
		return "Scheduled"
	case "active", "en-route":
		return "Airborne"
	case "landed":
		return "Landed"
	case "cancelled":
		return "Cancelled"
	case "incident":
		return "Incident"
	case "diverted":
		return "Diverted"
	default:
		if status == "" {
			return "Unknown"
		}
		return strings.ToUpper(status[:1]) + status[1:]
	}
}

// DelayTagLine returns delay fragments like "dep +20m", "on-time" when applicable.
func DelayTagLine(depDelay, arrDelay *int) string {
	var parts []string
	if depDelay != nil && *depDelay > 0 {
		parts = append(parts, fmt.Sprintf("dep +%dm", *depDelay))
	}
	if arrDelay != nil && *arrDelay > 0 {
		parts = append(parts, fmt.Sprintf("arr +%dm", *arrDelay))
	}
	if len(parts) == 0 {
		return "on-time"
	}
	return strings.Join(parts, " · ")
}

func bestDepTime(dep *endpointLeg) *time.Time {
	if dep == nil {
		return nil
	}
	if dep.Actual != nil {
		return dep.Actual
	}
	if dep.Estimated != nil {
		return dep.Estimated
	}
	return dep.Scheduled
}

func bestArrTime(arr *endpointLeg, status string) *time.Time {
	if arr == nil {
		return nil
	}
	st := strings.ToLower(status)
	if st == "landed" || st == "cancelled" || st == "incident" || st == "diverted" {
		if arr.Actual != nil {
			return arr.Actual
		}
	}
	if st == "active" || st == "en-route" {
		if arr.Estimated != nil {
			return arr.Estimated
		}
		if arr.Scheduled != nil {
			return arr.Scheduled
		}
	}
	if arr.Estimated != nil {
		return arr.Estimated
	}
	if arr.Scheduled != nil {
		return arr.Scheduled
	}
	return arr.Actual
}

// ProgressResult is travel completion 0–100% from schedule/estimates.
type ProgressResult struct {
	Percent   int
	Known     bool
	ShowBar   bool
	DenormMsg string
}

// ComputeProgress uses UTC instants in dep/arr for block time.
func ComputeProgress(status string, now time.Time, dep, arr *endpointLeg) ProgressResult {
	st := strings.ToLower(strings.TrimSpace(status))
	switch st {
	case "cancelled", "incident":
		return ProgressResult{Percent: 0, Known: true, ShowBar: false, DenormMsg: "n/a"}
	case "diverted":
		return ProgressResult{Percent: 0, Known: true, ShowBar: false, DenormMsg: "n/a"}
	case "landed":
		return ProgressResult{Percent: 100, Known: true, ShowBar: true, DenormMsg: ""}
	case "scheduled":
		return ProgressResult{Percent: 0, Known: true, ShowBar: true, DenormMsg: ""}
	case "active", "en-route":
		t0 := bestDepTime(dep)
		t1 := bestArrTime(arr, st)
		if t0 == nil || t1 == nil {
			return ProgressResult{Percent: 0, Known: false, ShowBar: false, DenormMsg: "time"}
		}
		total := t1.Sub(*t0)
		if total <= 0 {
			return ProgressResult{Percent: 0, Known: false, ShowBar: false, DenormMsg: "span"}
		}
		elapsed := now.Sub(*t0)
		pct := int(elapsed * 100 / total)
		if pct < 0 {
			pct = 0
		}
		if pct > 100 {
			pct = 100
		}
		return ProgressResult{Percent: pct, Known: true, ShowBar: true, DenormMsg: ""}
	default:
		return ProgressResult{Percent: 0, Known: false, ShowBar: false, DenormMsg: ""}
	}
}

// FormatDurationHrsMin formats a duration in minutes (rounded) for display.
func FormatDurationHrsMin(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	m := int(d.Minutes() + 0.5)
	if m < 1 {
		return "<1m"
	}
	if m < 60 {
		return fmt.Sprintf("%dm", m)
	}
	h := m / 60
	r := m % 60
	if r == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh %dm", h, r)
}

// FormatLocalInZone formats t in IANA zone as 24h local.
func FormatLocalInZone(t *time.Time, iana string) string {
	if t == nil {
		return "—"
	}
	loc, err := time.LoadLocation(iana)
	if err != nil || iana == "" {
		return t.UTC().Format("15:04Z") + " UTC"
	}
	return t.In(loc).Format("15:04")
}

// BlockDuration scheduled block dep→arr.
func BlockDuration(dep, arr *endpointLeg) (sched time.Duration, hasSched bool) {
	if dep == nil || arr == nil || dep.Scheduled == nil || arr.Scheduled == nil {
		return 0, false
	}
	d := arr.Scheduled.Sub(*dep.Scheduled)
	if d < 0 {
		return 0, false
	}
	return d, true
}

// ElapsedBlock is dep→arr using best times for the current phase.
func ElapsedBlock(dep, arr *endpointLeg, status string) (d time.Duration, ok bool) {
	d0 := bestDepTime(dep)
	d1 := bestArrTime(arr, status)
	if d0 == nil || d1 == nil {
		return 0, false
	}
	du := d1.Sub(*d0)
	if du < 0 {
		return 0, false
	}
	return du, true
}

// endpointDisplay builds "City, Country (IATA)" or fallbacks.
func endpointDisplay(leg *endpointLeg) string {
	if leg == nil {
		return "—"
	}
	parts := []string{}
	if leg.City != "" {
		parts = append(parts, leg.City)
	} else if leg.Airport != "" {
		parts = append(parts, leg.Airport)
	}
	if leg.Country != "" {
		if len(parts) == 0 {
			parts = append(parts, leg.Country)
		} else {
			parts[0] = parts[0] + ", " + leg.Country
		}
	} else if len(parts) == 0 && leg.IATA != "" {
		return leg.IATA
	}
	if leg.IATA != "" && len(parts) > 0 {
		return fmt.Sprintf("%s (%s)", strings.Join(parts, ""), leg.IATA)
	}
	if len(parts) > 0 {
		return parts[0]
	}
	if leg.Airport != "" {
		return leg.Airport
	}
	if leg.IATA != "" {
		return leg.IATA
	}
	return "—"
}
