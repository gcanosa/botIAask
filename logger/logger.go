package logger

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	logsDir = "logs"
	mu      sync.Mutex

	rotationMu   sync.RWMutex
	rotationDays int
	rotatorOnce  sync.Once
)

// LogEvent represents an enumeration of different types of IRC events
type EventType int

const (
	EventMessage EventType = iota
	EventJoin
	EventPart
	EventQuit
	EventKick
	EventNick
	EventAction
	EventNotice
	EventMode  // sender = who set it, message = "+o nick" (mode string + args)
	EventTopic // sender = who changed it, message = new topic
)

// LogChannelEvent logs an event to the daily channel log file.
// Format resembles traditional IRC logs.
func LogChannelEvent(serverName, channel string, eventType EventType, sender, message, target string) {
	if channel == "" || (channel[0] != '#' && channel[0] != '&') {
		// Possibly a private message, you can choose to log this or not.
		// For PMs, we use the "channel" as the target nick.
	}

	if err := os.MkdirAll(logsDir, 0755); err != nil {
		fmt.Printf("Error creating logs directory: %v\n", err)
		return
	}

	now := time.Now()
	dateStr := now.Format("2006-01-02")
	timeStr := now.Format("15:04:05")

	safeChannel := ChannelFileKey(channel, serverName)

	filename := fmt.Sprintf("%s_%s.log", safeChannel, dateStr)
	fullpath := filepath.Join(logsDir, filename)

	mu.Lock()
	defer mu.Unlock()

	f, err := os.OpenFile(fullpath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Printf("Error opening log file: %v\n", err)
		return
	}
	defer f.Close()

	var logLine string
	switch eventType {
	case EventMessage:
		logLine = fmt.Sprintf("[%s] <%s> %s\n", timeStr, sender, message)
	case EventNotice:
		logLine = fmt.Sprintf("[%s] -%s- %s\n", timeStr, sender, message)
	case EventAction:
		logLine = fmt.Sprintf("[%s] * %s %s\n", timeStr, sender, message)
	case EventJoin:
		logLine = fmt.Sprintf("[%s] *** %s has joined %s\n", timeStr, sender, channel)
	case EventPart:
		reason := message
		if reason == "" {
			logLine = fmt.Sprintf("[%s] *** %s has left %s\n", timeStr, sender, channel)
		} else {
			logLine = fmt.Sprintf("[%s] *** %s has left %s (%s)\n", timeStr, sender, channel, reason)
		}
	case EventQuit:
		reason := message
		logLine = fmt.Sprintf("[%s] *** %s has quit IRC (%s)\n", timeStr, sender, reason)
	case EventKick:
		reason := message
		logLine = fmt.Sprintf("[%s] *** %s was kicked by %s (%s)\n", timeStr, target, sender, reason)
	case EventNick:
		logLine = fmt.Sprintf("[%s] *** %s is now known as %s\n", timeStr, sender, message)
	case EventMode:
		logLine = fmt.Sprintf("[%s] *** %s sets mode %s\n", timeStr, sender, message)
	case EventTopic:
		logLine = fmt.Sprintf("[%s] *** %s changed the topic to: %s\n", timeStr, sender, message)
	default:
		logLine = fmt.Sprintf("[%s] <%s> %s\n", timeStr, sender, message)
	}

	f.WriteString(logLine)
}

// SetRotationDays updates how far back daily .log files are kept before archival.
// Values <= 0 disable rotation (existing files are not touched by the rotator).
func SetRotationDays(days int) {
	rotationMu.Lock()
	rotationDays = days
	rotationMu.Unlock()
}

func rotationDaysSnapshot() int {
	rotationMu.RLock()
	defer rotationMu.RUnlock()
	return rotationDays
}

// StartLogRotator starts a single background loop (once per process) that reads
// the current retention with SetRotationDays / config reloads.
func StartLogRotator(initial int) {
	SetRotationDays(initial)
	rotatorOnce.Do(func() {
		go logRotatorLoop()
	})
}

func logRotatorLoop() {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	runRotationIfEnabled()
	for range ticker.C {
		runRotationIfEnabled()
	}
}

func runRotationIfEnabled() {
	d := rotationDaysSnapshot()
	if d <= 0 {
		return
	}
	rotateLogs(d)
}

// rotateLogs deliberately does not take mu: only files older than the retention window are
// touched, and those are never being appended to (an append refreshes the mtime), so holding
// the global writer lock for the whole compression pass would just stall every LogChannelEvent.
func rotateLogs(days int) {
	entries, err := os.ReadDir(logsDir)
	if err != nil {
		fmt.Printf("Error reading logs directory for rotation: %v\n", err)
		return
	}

	archiveDir := filepath.Join(logsDir, "archive")
	if err := os.MkdirAll(archiveDir, 0755); err != nil {
		fmt.Printf("Error creating archive directory: %v\n", err)
		return
	}

	threshold := time.Now().AddDate(0, 0, -days)

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".log" {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		if info.ModTime().Before(threshold) {
			oldPath := filepath.Join(logsDir, entry.Name())
			newPath := filepath.Join(archiveDir, entry.Name()+".gz")

			if err := compressAndMoveLog(oldPath, newPath); err != nil {
				fmt.Printf("Error rotating log %s: %v\n", entry.Name(), err)
			}
		}
	}
}

func compressAndMoveLog(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}

	gz := gzip.NewWriter(out)
	_, err = io.Copy(gz, in)
	// Every close must succeed before the original is deleted: a failed flush leaves a
	// truncated archive, and removing src then would lose the log.
	if cerr := gz.Close(); err == nil {
		err = cerr
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dst)
		return err
	}

	return os.Remove(src)
}
