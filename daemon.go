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
func StartDaemon(cfg *config.Config) error {
	if !cfg.Daemon.Enabled {
		return fmt.Errorf("daemon mode is not enabled in configuration")
	}

	// Check if already running.
	pid, err := ReadPIDFile(cfg.Daemon.PIDFile)
	if err == nil {
		if IsProcessRunning(pid) {
			return fmt.Errorf("bot is already running (PID: %d)", pid)
		}
		// Stale PID file, remove it.
		DeletePIDFile(cfg.Daemon.PIDFile)
	}

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

	err = cmd.Start()
	if err != nil {
		return fmt.Errorf("failed to start daemon: %w", err)
	}

	fmt.Printf("Bot process spawned (Control PID: %d). Check %s for daemon status.\n", cmd.Process.Pid, cfg.Daemon.PIDFile)
	return nil
}

// StopDaemon handles stopping the bot by sending SIGTERM to the PID.
func StopDaemon(cfg *config.Config) error {
	pid, err := ReadPIDFile(cfg.Daemon.PIDFile)
	if err != nil {
		// If we can't read PID file, try to find process by other means
		return fmt.Errorf("failed to read PID file: %w", err)
	}

	if !IsProcessRunning(pid) {
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
				fmt.Println("Bot stopped")
				return nil
			}
		case <-timeout:
			// Force kill if still running.
			process.Signal(syscall.SIGKILL)
			DeletePIDFile(cfg.Daemon.PIDFile)
			fmt.Println("Bot force killed")
			return nil
		}
	}
}

// RestartDaemon stops and starts the bot.
func RestartDaemon(cfg *config.Config) error {
	err := StopDaemon(cfg)
	if err != nil {
		return fmt.Errorf("failed to stop bot: %w", err)
	}

	// Brief pause between stop and start.
	time.Sleep(1 * time.Second)

	return StartDaemon(cfg)
}
