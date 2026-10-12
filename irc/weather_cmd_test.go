package irc

import (
	"math"
	"strings"
	"testing"

	"botIAask/weather"
)

func TestDewPointMagnus(t *testing.T) {
	for _, c := range []struct {
		t    float64
		rh   int
		want float64
	}{{21, 80, 17.4}, {30, 20, 4.6}, {0, 100, 0}} {
		if got := dewPointC(c.t, c.rh); math.Abs(got-c.want) > 0.3 {
			t.Errorf("dew(%v,%d)=%.1f want %.1f", c.t, c.rh, got, c.want)
		}
	}
}

func TestBarCellsRounds(t *testing.T) {
	for pct, want := range map[int]int{0: 0, 4: 0, 5: 1, 67: 7, 91: 9, 95: 10, 100: 10} {
		if got := barCells(pct, 10); got != want {
			t.Errorf("barCells(%d)=%d want %d", pct, got, want)
		}
	}
}

func TestWeatherFallsBackTo3Days(t *testing.T) {
	s := &weather.Snapshot{
		Location: "Barcelona, Catalonia, Spain",
		Current:  &weather.Current{TempC: 21, ApparentC: 24, Humidity: 80, WindKmh: 3, Summary: "Partly cloudy", Icon: "partly"},
	}
	for _, d := range []string{"Mon", "Tue", "Wed", "Thu", "Fri"} {
		s.Daily = append(s.Daily, weather.Day{Weekday: d, Icon: "clear", MaxC: 24, MinC: 15})
	}
	full, _ := formatWeatherIRCLines(s, 1000)
	if !strings.Contains(full, "5d:") || strings.Contains(full, "open-meteo") {
		t.Fatalf("roomy budget: %q", full)
	}
	l1, l2 := formatWeatherIRCLines(s, len(full)-1)
	if l2 != "" || !strings.Contains(l1, "3d:") || len(l1) >= len(full) {
		t.Fatalf("tight budget: %q / %q", l1, l2)
	}
	l1, l2 = formatWeatherIRCLines(s, 100)
	if l2 == "" || !strings.Contains(l2, "5d:") {
		t.Fatalf("tiny budget should split: %q / %q", l1, l2)
	}
	if b := weatherTextBudget("botIAask", "#chan"); b < 350 || b > ircTextBudget {
		t.Fatalf("budget %d", b)
	}
	t.Logf("budget=%d len5=%d len3=%d\n%s", weatherTextBudget("botIAask", "#chan"), len(full), len(l1), full)
}
