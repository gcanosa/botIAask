// Package sysinfo collects lightweight host metrics for admin diagnostics (e.g. !stats).
package sysinfo

import (
	"time"

	"github.com/dustin/go-humanize"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
)

// Snapshot holds best-effort RAM and CPU readings.
type Snapshot struct {
	RAMAvailable string
	RAMUsedPct   float64
	CPUValid     bool
	CPUPct       float64
}

// Collect samples CPU usage over sample (defaults to 400ms if sample <= 0).
// RAM and CPU are collected independently; missing data leaves RAMAvailable empty or CPUValid false.
func Collect(sample time.Duration) Snapshot {
	var s Snapshot
	if v, err := mem.VirtualMemory(); err == nil {
		s.RAMAvailable = humanize.IBytes(v.Available)
		s.RAMUsedPct = v.UsedPercent
	}
	if sample <= 0 {
		sample = 400 * time.Millisecond
	}
	if pcts, err := cpu.Percent(sample, false); err == nil && len(pcts) > 0 {
		s.CPUValid = true
		s.CPUPct = pcts[0]
	}
	return s
}
