package irc

import (
	"sync"
	"time"
)

// ircevent has no outbound flood control, so a handler that prints a list (or several handlers at
// once) can get the bot killed with "Excess Flood". Every PRIVMSG/NOTICE goes through pacer.wait:
// a burst of defaultPaceBurst lines is free, after that one line per defaultPaceInterval.
const (
	defaultPaceBurst    = 5
	defaultPaceInterval = 500 * time.Millisecond
)

// pacer is a GCRA token bucket; the zero value uses the defaults above (tests set the fields).
type pacer struct {
	mu       sync.Mutex
	tat      time.Time // theoretical time at which the bucket is empty again
	burst    int
	interval time.Duration
}

// wait blocks until the caller may send one line. Callers on the IRC read loop must not send
// long runs of lines (move them to a goroutine) because this can sleep.
func (p *pacer) wait() {
	p.mu.Lock()
	burst, interval := p.burst, p.interval
	if burst <= 0 {
		burst = defaultPaceBurst
	}
	if interval <= 0 {
		interval = defaultPaceInterval
	}
	now := time.Now()
	if p.tat.Before(now) {
		p.tat = now
	}
	p.tat = p.tat.Add(interval)
	delay := p.tat.Sub(now) - time.Duration(burst)*interval
	p.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
}
