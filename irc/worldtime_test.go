package irc

import (
	"strings"
	"testing"

	"botIAask/config"
)

func TestTimeCommandListsEveryZone(t *testing.T) {
	n, out := newCaptureNetwork(t, &config.Config{})
	n.handleTimeCommand("#c")
	got := strings.Join(out.all(), " ")
	for _, city := range []string{"Honolulu", "Barcelona, Spain", "Auckland"} {
		if !strings.Contains(got, city) {
			t.Errorf("%s missing from %q", city, got)
		}
	}
	if len(worldTimeZones()) != len(worldTimeCities) {
		t.Errorf("zones loaded: %d of %d", len(worldTimeZones()), len(worldTimeCities))
	}
}
