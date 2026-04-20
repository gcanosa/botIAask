package crypto

import (
	"fmt"
	"math"
	"time"
)

const chartMaxPoints = 120

// ChartAPIResponse is JSON for /api/finance/crypto-chart.
type ChartAPIResponse struct {
	Range     string        `json:"range"`
	UpdatedAt string        `json:"updated_at"`
	Subtitle  string        `json:"subtitle"`
	Labels    []int64       `json:"labels"`
	Series    []ChartSeries `json:"series"`
}

// ChartSeries is one coin aligned to Labels (same length); nulls encode as JSON null.
type ChartSeries struct {
	Symbol string     `json:"symbol"`
	Values []*float64 `json:"values"`
}

// MarketRawSeries is raw OHLC-style points from market_chart.
type MarketRawSeries struct {
	Symbol  string
	GeckoID string
	Points  [][2]float64
}

// RangeToCoinGeckoDays maps UI range to the CoinGecko `days` query parameter.
func RangeToCoinGeckoDays(rangeKey string) (days string, err error) {
	switch rangeKey {
	case "6h":
		return "1", nil
	case "1d", "24h":
		return "1", nil
	case "3d":
		// CoinGecko is more reliable with >=7d; we still clip to 3d in BuildChartResponse.
		return "7", nil
	case "1w", "7d":
		return "7", nil
	case "3m", "90d":
		return "90", nil
	case "1y", "365d":
		return "365", nil
	default:
		return "", fmt.Errorf("unknown range %q", rangeKey)
	}
}

// RangeToWindow returns how far back to clip data (must be <= what CoinGecko returns for days).
func RangeToWindow(rangeKey string) (time.Duration, error) {
	switch rangeKey {
	case "6h":
		return 6 * time.Hour, nil
	case "1d", "24h":
		return 24 * time.Hour, nil
	case "3d":
		return 3 * 24 * time.Hour, nil
	case "1w", "7d":
		return 7 * 24 * time.Hour, nil
	case "3m", "90d":
		return 90 * 24 * time.Hour, nil
	case "1y", "365d":
		return 365 * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("unknown range %q", rangeKey)
	}
}

// NormalizeRangeKey accepts common aliases and returns a canonical key.
func NormalizeRangeKey(s string) string {
	switch s {
	case "6h", "6H":
		return "6h"
	case "1d", "24h", "1D":
		return "1d"
	case "3d", "3D":
		return "3d"
	case "1w", "7d", "1W":
		return "1w"
	case "3m", "90d", "3M":
		return "3m"
	case "1y", "365d", "1Y":
		return "1y"
	default:
		return s
	}
}

// BuildChartResponse aligns series to a shared time grid and converts to % change from each series start on that grid.
func BuildChartResponse(rangeKey string, raw []MarketRawSeries, now time.Time) (*ChartAPIResponse, error) {
	win, err := RangeToWindow(rangeKey)
	if err != nil {
		return nil, err
	}
	cutoff := now.Add(-win).UnixMilli()
	tMax := now.UnixMilli()

	filtered := make([]MarketRawSeries, 0, len(raw))
	for _, s := range raw {
		if len(s.Points) == 0 {
			continue
		}
		var clip [][2]float64
		for _, p := range s.Points {
			ms := int64(p[0])
			if ms < cutoff || ms > tMax {
				continue
			}
			clip = append(clip, p)
		}
		if len(clip) < 2 {
			continue
		}
		filtered = append(filtered, MarketRawSeries{Symbol: s.Symbol, GeckoID: s.GeckoID, Points: clip})
	}
	if len(filtered) == 0 {
		return &ChartAPIResponse{
			Range:     rangeKey,
			UpdatedAt: now.UTC().Format(time.RFC3339),
			Subtitle:  "% change from start of range (each coin indexed separately)",
			Labels:    []int64{},
			Series:    []ChartSeries{},
		}, nil
	}

	tMin := int64(filtered[0].Points[0][0])
	tMaxEff := int64(filtered[0].Points[len(filtered[0].Points)-1][0])
	for _, s := range filtered[1:] {
		if int64(s.Points[0][0]) > tMin {
			tMin = int64(s.Points[0][0])
		}
		last := int64(s.Points[len(s.Points)-1][0])
		if last < tMaxEff {
			tMaxEff = last
		}
	}
	if tMin >= tMaxEff {
		return nil, fmt.Errorf("empty time window after merge")
	}

	n := chartMaxPoints
	if n < 2 {
		n = 2
	}
	step := float64(tMaxEff-tMin) / float64(n-1)
	labels := make([]int64, n)
	for i := 0; i < n; i++ {
		labels[i] = tMin + int64(float64(i)*step+0.5)
	}

	out := &ChartAPIResponse{
		Range:     rangeKey,
		UpdatedAt: now.UTC().Format(time.RFC3339),
		Subtitle:  "% change from start of range (each coin indexed separately)",
		Labels:    labels,
		Series:    make([]ChartSeries, 0, len(filtered)),
	}

	for _, s := range filtered {
		prices := make([]float64, n)
		okAny := false
		for i, t := range labels {
			v, ok := linearInterpPrice(s.Points, float64(t))
			if !ok || v <= 0 || math.IsNaN(v) {
				prices[i] = math.NaN()
				continue
			}
			prices[i] = v
			okAny = true
		}
		if !okAny {
			continue
		}
		p0 := firstFinite(prices)
		if p0 <= 0 || math.IsNaN(p0) {
			continue
		}
		vals := make([]*float64, n)
		for i := range prices {
			if math.IsNaN(prices[i]) {
				vals[i] = nil
				continue
			}
			pct := (prices[i]/p0 - 1) * 100
			if math.IsNaN(pct) {
				vals[i] = nil
				continue
			}
			v := pct
			vals[i] = &v
		}
		out.Series = append(out.Series, ChartSeries{Symbol: s.Symbol, Values: vals})
	}

	return out, nil
}

func firstFinite(xs []float64) float64 {
	for _, x := range xs {
		if !math.IsNaN(x) && x > 0 {
			return x
		}
	}
	return math.NaN()
}

func linearInterpPrice(points [][2]float64, t float64) (float64, bool) {
	if len(points) == 0 {
		return 0, false
	}
	if t <= points[0][0] {
		return points[0][1], true
	}
	last := points[len(points)-1]
	if t >= last[0] {
		return last[1], true
	}
	for i := 0; i < len(points)-1; i++ {
		t0, p0 := points[i][0], points[i][1]
		t1, p1 := points[i+1][0], points[i+1][1]
		if t >= t0 && t <= t1 {
			if t1 == t0 {
				return p0, true
			}
			w := (t - t0) / (t1 - t0)
			return p0 + w*(p1-p0), true
		}
	}
	return 0, false
}
