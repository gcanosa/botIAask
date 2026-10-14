package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"botIAask/config"
)

// pidLock keeps the daemon's PID file flock'ed for the life of the process: the lock, not the pid
// number, is what proves a daemon is alive (a pid recorded before a crash or reboot may belong to an
// unrelated process by now).
var pidLock *os.File

// WritePIDFile locks the PID file and writes the current process ID into it. It fails when another
// instance already holds the lock.
func WritePIDFile(pidFile string) error {
	absPath, err := filepath.Abs(pidFile)
	if err != nil {
		return fmt.Errorf("failed to resolve absolute path for pid file: %w", err)
	}

	f, err := os.OpenFile(absPath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("failed to open pid file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return fmt.Errorf("pid file %s is locked: another instance is running", absPath)
	}
	if err := f.Truncate(0); err != nil {
		f.Close()
		return fmt.Errorf("failed to write pid file: %w", err)
	}
	if _, err := f.WriteAt([]byte(strconv.Itoa(os.Getpid())), 0); err != nil {
		f.Close()
		return fmt.Errorf("failed to write pid file: %w", err)
	}
	pidLock = f // deliberately never closed: the lock lives as long as the process
	return nil
}

// PIDFileInUse reports whether a running daemon holds the lock on pidFile.
func PIDFileInUse(pidFile string) bool {
	absPath, err := filepath.Abs(pidFile)
	if err != nil {
		return false
	}
	f, err := os.Open(absPath)
	if err != nil {
		return false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		return true // held exclusively by a live daemon
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

// ReadPIDFile reads the process ID from the specified file.
func ReadPIDFile(pidFile string) (int, error) {
	absPath, err := filepath.Abs(pidFile)
	if err != nil {
		return 0, fmt.Errorf("failed to resolve absolute path for pid file: %w", err)
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return 0, fmt.Errorf("failed to read pid file: %w", err)
	}

	var pid int
	_, err = fmt.Sscanf(string(data), "%d", &pid)
	if err != nil {
		return 0, fmt.Errorf("failed to parse pid from file: %w", err)
	}

	return pid, nil
}

// DeletePIDFile removes the PID file.
func DeletePIDFile(pidFile string) error {
	absPath, err := filepath.Abs(pidFile)
	if err != nil {
		return fmt.Errorf("failed to resolve absolute path for pid file: %w", err)
	}

	err = os.Remove(absPath)
	if err != nil {
		return fmt.Errorf("failed to delete pid file: %w", err)
	}

	return nil
}

// IsProcessRunning checks if a process with the given PID is running.
func IsProcessRunning(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	// On Unix systems, Signal(0) checks if process exists without sending a signal.
	err = process.Signal(syscall.Signal(0))
	return err == nil
}

// Daemonize forks the current process into daemon mode.
func Daemonize(cfg *config.Config) error {
	// Create a new command that will run the same binary with same arguments
	cmd := exec.Command(os.Args[0], os.Args[1:]...)

	// Redirect std streams to prevent issues
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil

	// Start the process in background
	err := cmd.Start()
	if err != nil {
		return fmt.Errorf("failed to start daemon: %w", err)
	}

	// The parent exits immediately after starting the child
	// This is how proper daemons work - we don't wait for the child

	fmt.Println("Bot started in daemon mode with PID:", cmd.Process.Pid)
	return nil
}

// StartDaemon handles starting the bot in daemon mode.
func StartDaemon(w io.Writer, cfg *config.Config, color bool) error {
	if !cfg.Daemon.Enabled {
		return fmt.Errorf("daemon mode is not enabled in configuration")
	}

	// Check if already running.
	if PIDFileInUse(cfg.Daemon.PIDFile) {
		pid, _ := ReadPIDFile(cfg.Daemon.PIDFile)
		return fmt.Errorf("bot is already running (PID: %d)", pid)
	}
	DeletePIDFile(cfg.Daemon.PIDFile) // stale (or absent) PID file

	// Filter out -mode start and -mode restart from child arguments to prevent loops
	var childArgs []string
	for i := 1; i < len(os.Args); i++ {
		arg := os.Args[i]
		if arg == "-mode" && i+1 < len(os.Args) {
			next := os.Args[i+1]
			if next == "start" || next == "restart" {
				i++ // skip the value
				continue
			}
		}
		if arg != "-daemon" { // we will add it explicitly
			childArgs = append(childArgs, arg)
		}
	}
	// Prepend -daemon flag
	childArgs = append([]string{"-daemon"}, childArgs...)

	// Fork the process using exec to create a proper daemon
	cmd := exec.Command(os.Args[0], childArgs...)

	// Set environment variable to identify the child as the daemon instance
	cmd.Env = append(os.Environ(), "BOT_DAEMON_INTERNAL=1")

	// Setsid detaches the process from the controlling terminal (important for daemons)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}

	// Detach standard streams
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start daemon: %w", err)
	}

	printDaemonSpawnResult(w, cfg, cmd.Process.Pid, color)
	return nil
}

// StopDaemon handles stopping the bot by sending SIGTERM to the PID.
func StopDaemon(w io.Writer, cfg *config.Config, color bool) error {
	pid, err := ReadPIDFile(cfg.Daemon.PIDFile)
	if err != nil {
		// If we can't read PID file, try to find process by other means
		return fmt.Errorf("failed to read PID file: %w", err)
	}

	// A pid alone is not proof: it may have been reused by an unrelated process since a crash.
	if !PIDFileInUse(cfg.Daemon.PIDFile) {
		DeletePIDFile(cfg.Daemon.PIDFile)
		return fmt.Errorf("bot is not running (stale PID file removed)")
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("failed to find process: %w", err)
	}

	err = process.Signal(syscall.SIGTERM)
	if err != nil {
		return fmt.Errorf("failed to send SIGTERM: %w", err)
	}

	// Wait for process to exit.
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case <-ticker.C:
			err := process.Signal(syscall.Signal(0))
			if err != nil {
				DeletePIDFile(cfg.Daemon.PIDFile)
				printStopResult(w, cfg, pid, StopExitedNormal, color)
				return nil
			}
		case <-timeout:
			// Force kill if still running.
			process.Signal(syscall.SIGKILL)
			DeletePIDFile(cfg.Daemon.PIDFile)
			printStopResult(w, cfg, pid, StopForceKilled, color)
			return nil
		}
	}
}

// RestartDaemon stops and starts the bot.
func RestartDaemon(w io.Writer, cfg *config.Config, color bool) error {
	err := StopDaemon(w, cfg, color)
	if err != nil {
		return fmt.Errorf("failed to stop bot: %w", err)
	}

	// Brief pause between stop and start.
	time.Sleep(1 * time.Second)

	return StartDaemon(w, cfg, color)
}
