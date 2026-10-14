package irc

import (
	"testing"
	"time"
)

func TestPacerBurstThenSpacing(t *testing.T) {
	p := pacer{burst: 3, interval: 40 * time.Millisecond}
	start := time.Now()
	for i := 0; i < 3; i++ {
		p.wait()
	}
	if d := time.Since(start); d > 30*time.Millisecond {
		t.Fatalf("first 3 (the burst) took %v, want ~0", d)
	}
	start = time.Now()
	for i := 0; i < 3; i++ {
		p.wait()
	}
	if d := time.Since(start); d < 90*time.Millisecond {
		t.Fatalf("3 lines past the burst took %v, want >= 3 intervals (~120ms)", d)
	}
}

func TestPacerZeroValueUsesDefaults(t *testing.T) {
	var p pacer
	start := time.Now()
	for i := 0; i < defaultPaceBurst; i++ {
		p.wait()
	}
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Fatalf("default burst should be free, took %v", d)
	}
}
