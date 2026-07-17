// Package guard runs background goroutines with panic recovery so a bug in
// one long-lived loop (RSS, stats, backups, command handling) can't take
// down the whole process.
package guard

import (
	"log"
	"runtime/debug"
)

// Go runs fn in a new goroutine; a panic inside fn is recovered and logged
// instead of crashing the process. name identifies the goroutine in logs.
func Go(name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("panic in %s: %v\n%s", name, r, debug.Stack())
			}
		}()
		fn()
	}()
}
