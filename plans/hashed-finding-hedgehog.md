# Plan: Add Daemon Mode Support to IRC Bot

## Context

The IRC bot currently runs as a foreground process that blocks indefinitely. Users want to be able to run the bot in daemon mode using `go run main.go -daemon` and have it create a PID file (`bot.pid`) for process management. By default, the bot should run in daemon mode, but `-debug` flag should enable stdout logging and keep the process in foreground.

## Implementation Approach

### 1. Command-Line Flag Handling
- Add `-daemon` flag to enable background process mode
- Add `-debug` flag to enable foreground mode with stdout logging
- Default behavior: run in daemon mode (background)
- When `-debug` is specified, run in foreground with full console output

### 2. Process Management
- Implement proper daemon process detachment using os/exec and syscall libraries
- Create PID file (`bot.pid`) containing process ID when running in daemon mode
- Ensure proper file permissions for PID file
- Handle cleanup of PID file on graceful shutdown

### 3. Signal Handling
- Add signal handlers for SIGTERM and SIGINT to enable graceful shutdown
- When receiving termination signals, disconnect from IRC and clean up resources
- Remove PID file during shutdown

### 4. Logging Adjustments
- In daemon mode: redirect logs to file or disable console output
- In debug mode: maintain current console logging behavior
- Ensure all error messages are properly logged

### 5. Configuration Considerations
- No changes needed to config files
- All process management is handled at runtime via flags

## Critical Files to Modify

1. **main.go** - Main entry point for flag parsing, process management, and signal handling
2. **irc/bot.go** - May need minor adjustments for graceful shutdown handling

## Implementation Details

### Flag Definition
```go
daemon := flag.Bool("daemon", true, "Run bot in daemon mode")
debug := flag.Bool("debug", false, "Enable debug mode with console output")
```

### Process Detachment Logic
- Use syscall.ForkExec or similar to detach from controlling terminal
- Redirect stdin/stdout/stderr to /dev/null (Unix) or NUL (Windows)
- Create PID file in working directory

### Signal Handling
```go
c := make(chan os.Signal, 1)
signal.Notify(c, os.Interrupt, syscall.SIGTERM)
// Handle shutdown gracefully
```

### PID File Management
- Create `bot.pid` file with process ID when daemon starts
- Remove file on graceful shutdown
- Handle permission errors appropriately

## Verification Approach

1. Build the bot: `go build -o bot main.go`
2. Run in daemon mode: `./bot -daemon`
3. Verify PID file is created: `cat bot.pid`
4. Verify process is running in background
5. Test graceful shutdown with SIGTERM: `kill $(cat bot.pid)`
6. Test debug mode: `./bot -debug`
7. Verify console output in debug mode
8. Ensure all existing functionality remains intact

## Backward Compatibility
- Default behavior unchanged (daemon mode enabled by default)
- All existing features and commands preserved
- No configuration file changes required