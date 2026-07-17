package stats

import (
	"sync"
	"testing"

	"botIAask/config"
)

// TestApplyConfigRace hammers ApplyConfig (which used to write t.cfg outside subMu)
// concurrently with LogMessage/IsEnabled. Run with -race.
func TestApplyConfigRace(t *testing.T) {
	tr := NewTracker(&config.Config{Stats: config.StatsConfig{Enabled: false}}, nil)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				tr.LogMessage("nick")
				_ = tr.IsEnabled()
			}
		}()
	}

	for i := 0; i < 100; i++ {
		tr.ApplyConfig(&config.Config{Stats: config.StatsConfig{Enabled: i%2 == 0, Interval: 60}})
	}

	close(stop)
	wg.Wait()
}
