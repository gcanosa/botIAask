package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"botIAask/config"
)

// DaemonConfig holds daemon-specific configuration.
type DaemonConfig struct {
	Enabled  bool   `yaml:"enabled"`
	PIDFile  string `yaml:"pid_file"`
}

// WritePIDFile writes the current process ID to the specified file.
func WritePIDFile(pidFile string) error {
	pid := os.Getpid()
	absPath, err := filepath.Abs(pidFile)
	if err != nil {
		return fmt.Errorf("failed to resolve absolute path for pid file: %w", err)
	}

	err = os.WriteFile(absPath, []byte(fmt.Sprintf("%d", pid)), 0644)
	if err != nil {
		return fmt.Errorf("failed to write pid file: %w", err)
	}

	return nil
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
func Daemonize() error {
	// Check if already running by trying to lock the PID file.
	cmd := exec.Command(os.Args[0], os.Args[1:]...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil

	err := cmd.Start()
	if err != nil {
		return fmt.Errorf("failed to start daemon: %w", err)
	}

	// Give the child process a moment to write its PID file.
	// If it fails to write, the parent should clean up.
	go func() {
		time.Sleep(2 * time.Second)
		// If the child is still running and has a PID file, we're good.
		// Otherwise, clean up our own PID file if it exists.
		absPath, _ := filepath.Abs(config.Daemon.PIDFile)
		if _, err := os.Stat(absPath); err == nil {
			pid, err := ReadPIDFile(config.Daemon.PIDFile)
			if err == nil && IsProcessRunning(pid) {
				return // Child is running fine, exit parent.
			}
		}
		// Child failed to start properly, clean up.
		os.Remove(absPath)
	}()

	// Exit the parent process immediately.
	os.Exit(0)
}

// StartDaemon handles starting the bot in daemon mode.
func StartDaemon() error {
	if !config.Daemon.Enabled {
		return fmt.Errorf("daemon mode is not enabled in configuration")
	}

	// Check if already running.
	pid, err := ReadPIDFile(config.Daemon.PIDFile)
	if err == nil {
		if IsProcessRunning(pid) {
			return fmt.Errorf("bot is already running (PID: %d)", pid)
		}
		// Stale PID file, remove it.
		DeletePIDFile(config.Daemon.PIDFile)
	}

	err = Daemonize()
	if err != nil {
		return fmt.Errorf("failed to daemonize: %w", err)
	}

	fmt.Println("Bot started in daemon mode")
	return nil
}

// StopDaemon handles stopping the bot by sending SIGTERM to the PID.
func StopDaemon() error {
	pid, err := ReadPIDFile(config.Daemon.PIDFile)
	if err != nil {
		return fmt.Errorf("failed to read PID file: %w", err)
	}

	if !IsProcessRunning(pid) {
		DeletePIDFile(config.Daemon.PIDFile)
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
				DeletePIDFile(config.Daemon.PIDFile)
				fmt.Println("Bot stopped")
				return nil
			}
		case <-timeout:
			// Force kill if still running.
			process.Signal(syscall.SIGKILL)
			DeletePIDFile(config.Daemon.PIDFile)
			fmt.Println("Bot force killed")
			return nil
		}
	}
}

// RestartDaemon stops and starts the bot.
func RestartDaemon() error {
	err := StopDaemon()
	if err != nil {
		return fmt.Errorf("failed to stop bot: %w", err)
	}

	// Brief pause between stop and start.
	time.Sleep(1 * time.Second)

	return StartDaemon()
}
