package guard

import (
	"testing"
	"time"
)

func TestGoRecoversPanic(t *testing.T) {
	done := make(chan struct{})
	Go("test", func() {
		defer close(done)
		panic("boom")
	})
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("goroutine never ran")
	}
	time.Sleep(20 * time.Millisecond) // process is still alive if we get here
}

func TestWrapRecoversPanicAndRunsFn(t *testing.T) {
	ran := false
	Wrap("test", func() { ran = true; panic("boom") })()
	if !ran {
		t.Fatal("fn not run")
	}
}
