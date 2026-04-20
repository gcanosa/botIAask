package crypto

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// BuildForexChartResponse aligns locally stored forex snapshots to a shared time grid (absolute rates).
func BuildForexChartResponse(rangeKey string, rows []ForexHistoryRow, now time.Time) (*ChartAPIResponse, error) {
	win, err := RangeToWindow(rangeKey)
	if err != nil {
		return nil, err
	}
	cutoff := now.Add(-win).UnixMilli()
	tMax := now.UnixMilli()

	byKey := make(map[string][][2]float64)
	for _, r := range rows {
		ms := r.FetchedAt.UnixMilli()
		if ms < cutoff || ms > tMax {
			continue
		}
		byKey[r.Key] = append(byKey[r.Key], [2]float64{float64(ms), r.Value})
	}

	for k, pts := range byKey {
		sort.Slice(pts, func(i, j int) bool { return pts[i][0] < pts[j][0] })
		// collapse duplicate timestamps (keep last)
		compact := make([][2]float64, 0, len(pts))
		for _, p := range pts {
			if len(compact) > 0 && compact[len(compact)-1][0] == p[0] {
				compact[len(compact)-1] = p
				continue
			}
			compact = append(compact, p)
		}
		byKey[k] = compact
	}

	filtered := make([]struct {
		key    string
		points [][2]float64
	}, 0, len(byKey))
	for key, pts := range byKey {
		var clip [][2]float64
		for _, p := range pts {
			ms := int64(p[0])
			if ms < cutoff || ms > tMax {
				continue
			}
			clip = append(clip, p)
		}
		if len(clip) < 2 {
			continue
		}
		filtered = append(filtered, struct {
			key    string
			points [][2]float64
		}{key: key, points: clip})
	}

	if len(filtered) == 0 {
		return &ChartAPIResponse{
			Range:     rangeKey,
			UpdatedAt: now.UTC().Format(time.RFC3339),
			Subtitle:  "No local history in this range yet (snapshots accumulate hourly).",
			Labels:    []int64{},
			Series:    []ChartSeries{},
		}, nil
	}

	tMin := int64(filtered[0].points[0][0])
	tMaxEff := int64(filtered[0].points[len(filtered[0].points)-1][0])
	for _, s := range filtered[1:] {
		if int64(s.points[0][0]) > tMin {
			tMin = int64(s.points[0][0])
		}
		last := int64(s.points[len(s.points)-1][0])
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
		Subtitle:  "Historic values from local snapshots (same range as above)",
		Labels:    labels,
		Series:    make([]ChartSeries, 0, len(filtered)),
	}

	for _, s := range filtered {
		vals := make([]*float64, n)
		okAny := false
		for i, t := range labels {
			v, ok := linearInterpPrice(s.points, float64(t))
			if !ok || math.IsNaN(v) || v <= 0 {
				vals[i] = nil
				continue
			}
			okAny = true
			vv := v
			vals[i] = &vv
		}
		if !okAny {
			continue
		}
		out.Series = append(out.Series, ChartSeries{Symbol: s.key, Values: vals})
	}

	return out, nil
}
