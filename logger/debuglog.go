package logger

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"

	"botIAask/internal/securefs"
)

// DebugDir holds the bot's own process log (log.Printf output: connects, reconnects, errors,
// recovered panics) and fatal crash traces, one file per day. IRC traffic stays in logsDir.
func DebugDir() string { return filepath.Join(logsDir, "debug") }

// dailyFile is an io.Writer appending to <dir>/bot_<date>.log, reopening when the day changes.
type dailyFile struct {
	mu   sync.Mutex
	dir  string
	date string
	f    *os.File
}

func (d *dailyFile) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if today := time.Now().Format("2006-01-02"); today != d.date || d.f == nil {
		f, err := os.OpenFile(filepath.Join(d.dir, "bot_"+today+".log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, securefs.FileMode())
		if err != nil {
			return 0, err
		}
		if d.f != nil {
			d.f.Close()
		}
		d.f, d.date = f, today
		// Unrecovered panics and runtime fatal errors bypass the log package; the runtime writes
		// them here too (in addition to stderr) before the process dies.
		_ = debug.SetCrashOutput(f, debug.CrashOptions{})
	}
	return d.f.Write(p)
}

// StartDebugLog tees the standard logger into logs/debug/bot_<date>.log, keeping stderr (the
// terminal, or `podman logs` in a container; /dev/null for the forked daemon). Old files are
// archived and purged by the log rotator like channel logs.
func StartDebugLog() error {
	dir := DebugDir()
	if err := securefs.Mkdir(logsDir); err != nil {
		return err
	}
	if err := securefs.Mkdir(dir); err != nil {
		return err
	}
	w := &dailyFile{dir: dir}
	if _, err := fmt.Fprintf(w, "%s --- process start, PID %d ---\n", time.Now().Format("2006/01/02 15:04:05"), os.Getpid()); err != nil {
		return err
	}
	log.SetOutput(io.MultiWriter(os.Stderr, w))
	return nil
}
