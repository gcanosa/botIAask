package logger

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"botIAask/internal/guard"
	"botIAask/internal/securefs"
)

var (
	logsDir = "logs"
	mu      sync.Mutex

	rotationMu   sync.RWMutex
	rotationDays int
	archiveDays  int
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

// logHandleIdle is how long an open log file may sit unused before its handle is closed.
const logHandleIdle = 10 * time.Minute

// logHandles caches one open append handle per day file. Opening, writing and closing a file for
// every single line (plus a MkdirAll) under the global lock made every event cost four syscalls
// and serialised all channels behind disk latency; a netsplit writes thousands of QUIT lines.
type logHandle struct {
	f    *os.File
	used time.Time
}

var (
	logHandles    = map[string]*logHandle{} // guarded by mu
	lastHandleGC  time.Time
	logsDirExists bool
)

// openLogLocked returns the append handle for path, opening it (and creating logs/) on first use.
// Caller holds mu.
func openLogLocked(path string, now time.Time) (*os.File, error) {
	if h, ok := logHandles[path]; ok {
		h.used = now
		return h.f, nil
	}
	if !logsDirExists {
		if err := securefs.Mkdir(logsDir); err != nil {
			return nil, err
		}
		logsDirExists = true
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, securefs.FileMode())
	if errors.Is(err, fs.ErrNotExist) { // logs/ was removed while we ran
		if err = securefs.Mkdir(logsDir); err == nil {
			f, err = os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, securefs.FileMode())
		}
	}
	if err != nil {
		return nil, err
	}
	logHandles[path] = &logHandle{f: f, used: now}
	return f, nil
}

// closeIdleLocked closes handles unused for logHandleIdle (yesterday's files after midnight,
// channels that went quiet). Runs at most once a minute. Caller holds mu.
func closeIdleLocked(now time.Time) {
	if now.Sub(lastHandleGC) < time.Minute {
		return
	}
	lastHandleGC = now
	for path, h := range logHandles {
		if now.Sub(h.used) > logHandleIdle {
			h.f.Close()
			delete(logHandles, path)
		} else if _, err := os.Stat(path); err != nil {
			// The file was removed or rotated away under us: stop writing into the unlinked inode.
			h.f.Close()
			delete(logHandles, path)
		}
	}
}

// CloseAll closes every cached log handle (shutdown, and tests that change directory).
func CloseAll() {
	mu.Lock()
	defer mu.Unlock()
	for path, h := range logHandles {
		h.f.Close()
		delete(logHandles, path)
	}
	logsDirExists = false
}

// LogChannelEvent logs an event to the daily channel log file.
// Format resembles traditional IRC logs.
func LogChannelEvent(serverName, channel string, eventType EventType, sender, message, target string) {
	now := time.Now()
	dateStr := now.Format("2006-01-02")
	timeStr := now.Format("15:04:05")

	safeChannel := ChannelFileKey(channel, serverName)

	filename := fmt.Sprintf("%s_%s.log", safeChannel, dateStr)
	fullpath := filepath.Join(logsDir, filename)

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

	mu.Lock()
	defer mu.Unlock()
	closeIdleLocked(now)
	f, err := openLogLocked(fullpath, now)
	if err != nil {
		log.Printf("Error opening log file: %v\n", err)
		return
	}
	// One write per line to an O_APPEND file: readers (web tail, rollups) never see half a line.
	if _, err := f.WriteString(logLine); err != nil {
		log.Printf("Error writing log file: %v\n", err)
		f.Close() // reopen on the next line (disk was full, file was removed, ...)
		delete(logHandles, fullpath)
	}
}

// SetRotationDays updates how far back daily .log files are kept before archival.
// Values <= 0 disable rotation (existing files are not touched by the rotator).
func SetRotationDays(days int) {
	rotationMu.Lock()
	rotationDays = days
	rotationMu.Unlock()
}

// SetArchiveDays sets how many days (counted from the log's own date) archived .log.gz files
// are kept before being deleted. Values <= 0 keep archives forever.
func SetArchiveDays(days int) {
	rotationMu.Lock()
	archiveDays = days
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
		guard.Go("log rotator", logRotatorLoop)
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
	rotationMu.RLock()
	d, a := rotationDays, archiveDays
	rotationMu.RUnlock()
	for _, dir := range []string{logsDir, DebugDir()} {
		if d > 0 {
			rotateLogs(dir, d)
		}
		if a > 0 {
			purgeArchives(dir, a, time.Now())
		}
	}
}

// purgeArchives deletes <dir>/archive/*_<YYYY-MM-DD>.log.gz files whose date is more than days
// before now. The date comes from the file name (the day logged), not the mtime, which is
// just when rotation compressed it.
func purgeArchives(dir string, days int, now time.Time) {
	archiveDir := filepath.Join(dir, "archive")
	entries, err := os.ReadDir(archiveDir)
	if err != nil {
		return
	}
	cutoff := now.AddDate(0, 0, -days).Format("2006-01-02")
	for _, e := range entries {
		name := e.Name()
		base, ok := strings.CutSuffix(name, ".log.gz")
		if e.IsDir() || !ok || len(base) < 10 {
			continue
		}
		date := base[len(base)-10:]
		if _, err := time.Parse("2006-01-02", date); err != nil || date >= cutoff {
			continue
		}
		if err := os.Remove(filepath.Join(archiveDir, name)); err != nil {
			log.Printf("Error deleting old log archive %s: %v\n", name, err)
		}
	}
}

// rotateLogs deliberately does not take mu: only files older than the retention window are
// touched, and those are never being appended to (an append refreshes the mtime), so holding
// the global writer lock for the whole compression pass would just stall every LogChannelEvent.
func rotateLogs(dir string, days int) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		log.Printf("Error reading logs directory for rotation: %v\n", err)
		return
	}

	archiveDir := filepath.Join(dir, "archive")
	if err := securefs.Mkdir(archiveDir); err != nil {
		log.Printf("Error creating archive directory: %v\n", err)
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
			oldPath := filepath.Join(dir, entry.Name())
			newPath := filepath.Join(archiveDir, entry.Name()+".gz")

			if err := compressAndMoveLog(oldPath, newPath); err != nil {
				log.Printf("Error rotating log %s: %v\n", entry.Name(), err)
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
