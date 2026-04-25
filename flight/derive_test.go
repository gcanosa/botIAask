package flight

import (
	"testing"
	"time"
)

func TestComputeProgress(t *testing.T) {
	depT := time.Date(2026, 4, 25, 10, 0, 0, 0, time.UTC)
	arrT := time.Date(2026, 4, 25, 14, 0, 0, 0, time.UTC)
	dep := &endpointLeg{Scheduled: &depT}
	arr := &endpointLeg{Scheduled: &arrT, Estimated: &arrT}
	p := ComputeProgress("scheduled", time.Date(2026, 4, 25, 9, 0, 0, 0, time.UTC), dep, arr)
	if p.Percent != 0 || !p.Known || !p.ShowBar {
		t.Fatalf("scheduled: %+v", p)
	}
	p = ComputeProgress("landed", depT, dep, arr)
	if p.Percent != 100 || !p.Known || !p.ShowBar {
		t.Fatalf("landed: %+v", p)
	}
	mid := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	actDep := depT
	estArr := arrT
	dep2 := &endpointLeg{Scheduled: &depT, Actual: &actDep}
	arr2 := &endpointLeg{Scheduled: &arrT, Estimated: &estArr}
	p = ComputeProgress("active", mid, dep2, arr2)
	if p.Percent < 45 || p.Percent > 55 {
		t.Fatalf("active mid: want ~50%% got %d", p.Percent)
	}
}

func TestDelayTagLine(t *testing.T) {
	a, b := 3, 5
	if got := DelayTagLine(&a, &b); got != "dep +3m · arr +5m" {
		t.Fatalf("got %q", got)
	}
	if got := DelayTagLine(nil, nil); got != "on-time" {
		t.Fatalf("got %q", got)
	}
}

func TestEndpointDisplay(t *testing.T) {
	leg := &endpointLeg{City: "Lima", Country: "Peru", IATA: "LIM"}
	if got := endpointDisplay(leg); got != "Lima, Peru (LIM)" {
		t.Fatalf("got %q", got)
	}
}
