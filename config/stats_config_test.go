package config

import "testing"

func TestStatsConfig_ShouldSaveToDB(t *testing.T) {
	f := false
	tt := true
	t.Run("omitted when enabled defaults true", func(t *testing.T) {
		s := StatsConfig{Enabled: true, SaveToDB: nil}
		if !s.ShouldSaveToDB() {
			t.Fatal("expected true")
		}
	})
	t.Run("explicit false", func(t *testing.T) {
		s := StatsConfig{Enabled: true, SaveToDB: &f}
		if s.ShouldSaveToDB() {
			t.Fatal("expected false")
		}
	})
	t.Run("explicit true", func(t *testing.T) {
		s := StatsConfig{Enabled: true, SaveToDB: &tt}
		if !s.ShouldSaveToDB() {
			t.Fatal("expected true")
		}
	})
	t.Run("off when stats disabled and omitted", func(t *testing.T) {
		s := StatsConfig{Enabled: false, SaveToDB: nil}
		if s.ShouldSaveToDB() {
			t.Fatal("expected false when stats disabled and save_to_db omitted")
		}
	})
}
