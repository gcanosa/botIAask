package crypto

import (
	"testing"
	"time"
)

func TestBuildForexChartResponse_usesFullRangeWindow(t *testing.T) {
	now := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
	win, err := RangeToWindow("1w")
	if err != nil {
		t.Fatal(err)
	}
	cutoff := now.Add(-win).UnixMilli()

	// eur_usd: samples from early in the window
	// usd_ars: only recent samples (old logic would shrink X-axis to max of these starts)
	rows := []ForexHistoryRow{
		{Key: "eur_usd", Value: 1.0, FetchedAt: time.UnixMilli(cutoff).Add(time.Hour)},
		{Key: "eur_usd", Value: 1.1, FetchedAt: now.Add(-10 * time.Hour)},
		{Key: "usd_ars", Value: 100.0, FetchedAt: now.Add(-20 * time.Hour)},
		{Key: "usd_ars", Value: 101.0, FetchedAt: now.Add(-1 * time.Hour)},
	}

	resp, err := BuildForexChartResponse("1w", rows, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Labels) < 2 {
		t.Fatalf("expected labels, got %d", len(resp.Labels))
	}

	if got := resp.Labels[0]; got != cutoff {
		t.Fatalf("first label = %d, want cutoff %d", got, cutoff)
	}
	last := resp.Labels[len(resp.Labels)-1]
	want := now.UnixMilli()
	if last < want-2 || last > want+2 {
		t.Fatalf("last label = %d, want ~now %d", last, want)
	}

	span := last - resp.Labels[0]
	if span < win.Milliseconds()-10 {
		t.Fatalf("label span %d shorter than window %d", span, win.Milliseconds())
	}

	// Regression: intersection of series starts would begin near the later pair's first point (~now-20h), not at cutoff.
	intersectionStart := now.Add(-20 * time.Hour).UnixMilli()
	if resp.Labels[0] >= intersectionStart {
		t.Fatalf("grid should start before intersection-only start %d, got %d", intersectionStart, resp.Labels[0])
	}

	if len(resp.Series) != 2 {
		t.Fatalf("expected 2 series, got %d", len(resp.Series))
	}
}
