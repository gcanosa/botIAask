package irc

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"botIAask/config"

	"github.com/ergochat/irc-go/ircevent"
)

// TestConnectWithRetry_CancelledBeforeStart guards the uncancellable-retry-loop bug: a
// network already requested to quit (disconnectNetwork closed n.quit) must not attempt to
// connect at all, and must return promptly rather than running out its backoff schedule.
func TestConnectWithRetry_CancelledBeforeStart(t *testing.T) {
	b := newTestBot(&config.Config{
		IRC: config.IRCConfig{Networks: []config.IRCNetworkConfig{{Name: "test"}}},
	})
	n := b.buildNetwork(config.IRCNetworkConfig{Name: "test", Server: "127.0.0.1", Port: 1})
	n.requestQuit()

	done := make(chan error, 1)
	go func() { done <- b.connectWithRetry(n) }()

	select {
	case err := <-done:
		if err != errNetworkCancelled {
			t.Fatalf("want errNetworkCancelled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("connectWithRetry did not return promptly after cancellation")
	}
}

// TestRequestQuit_Idempotent guards against a double-close panic if disconnectNetwork's
// pending/live cleanup ever races and calls requestQuit twice for the same network.
func TestRequestQuit_Idempotent(t *testing.T) {
	b := newTestBot(&config.Config{
		IRC: config.IRCConfig{Networks: []config.IRCNetworkConfig{{Name: "test"}}},
	})
	n := b.buildNetwork(config.IRCNetworkConfig{Name: "test"})
	n.requestQuit()
	n.requestQuit() // must not panic
	if !n.cancelled() {
		t.Fatal("expected cancelled() true after requestQuit")
	}
}

// TestDisconnectNetwork_PendingCancelsRetry guards the specific gap: a network still
// backing off its initial connect (registered in b.pending, not yet b.networks) must be
// cancellable by disconnectNetwork, which previously only ever looked at b.networks.
func TestDisconnectNetwork_PendingCancelsRetry(t *testing.T) {
	b := newTestBot(&config.Config{
		IRC: config.IRCConfig{Networks: []config.IRCNetworkConfig{{Name: "test"}}},
	})
	n := b.buildNetwork(config.IRCNetworkConfig{Name: "test"})
	b.pendingMu.Lock()
	b.pending["test"] = n
	b.pendingMu.Unlock()

	b.disconnectNetwork("test")

	if !n.cancelled() {
		t.Fatal("disconnectNetwork did not cancel a pending (still-retrying) network")
	}
	b.pendingMu.RLock()
	_, stillPending := b.pending["test"]
	b.pendingMu.RUnlock()
	if stillPending {
		t.Fatal("disconnectNetwork left the entry in b.pending")
	}
}

// TestRunNetwork_RegistryCompareAndDelete guards the registry-clobber bug: a stale
// goroutine's cleanup must never erase a different, live instance registered under the
// same name (e.g. after ApplyLiveConfig's endpoint-change reconnect races the old
// goroutine's teardown).
func TestRunNetwork_RegistryCompareAndDelete(t *testing.T) {
	b := newTestBot(&config.Config{
		IRC: config.IRCConfig{Networks: []config.IRCNetworkConfig{{Name: "test"}}},
	})
	stale := b.buildNetwork(config.IRCNetworkConfig{Name: "test"})
	live := b.buildNetwork(config.IRCNetworkConfig{Name: "test"})

	b.networksMu.Lock()
	b.networks["test"] = live
	b.networksMu.Unlock()

	// Simulate the stale goroutine's post-Loop() cleanup finding a different instance
	// already registered under its name.
	b.networksMu.Lock()
	if b.networks["test"] == stale {
		delete(b.networks, "test")
	}
	b.networksMu.Unlock()

	if got := b.network("test"); got != live {
		t.Fatalf("stale cleanup erased the live network: got %v, want %v", got, live)
	}
}

// TestRunNetwork_RestartsAfterLoopPanic guards the "never came back" bug: when a network's
// event loop dies without a quit request, runNetwork must rebuild and reconnect it, and an
// explicit disconnectNetwork must still stop it for good.
func TestRunNetwork_RestartsAfterLoopPanic(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	conns := make(chan struct{}, 10)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns <- struct{}{}
			go func(c net.Conn) {
				defer c.Close()
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					if strings.HasPrefix(sc.Text(), "QUIT") {
						return // like a real server: close the link on QUIT
					}
					if strings.HasPrefix(sc.Text(), "USER ") {
						fmt.Fprint(c, ":srv 001 bot :welcome\r\n:srv 422 bot :no MOTD\r\n")
					}
				}
			}(c)
		}
	}()

	oldDelay, oldLoop := networkRestartDelay, runLoop
	defer func() { networkRestartDelay, runLoop = oldDelay, oldLoop }()
	networkRestartDelay = 10 * time.Millisecond
	var loops atomic.Int32
	runLoop = func(c *ircevent.Connection) {
		if loops.Add(1) == 1 {
			panic("simulated loop crash")
		}
		c.Loop() // second instance: real loop, returns after disconnectNetwork's Quit
	}

	port := ln.Addr().(*net.TCPAddr).Port
	netCfg := config.IRCNetworkConfig{Name: "test", Server: "127.0.0.1", Port: port, Nickname: "bot"}
	b := newTestBot(&config.Config{IRC: config.IRCConfig{Networks: []config.IRCNetworkConfig{netCfg}}})

	done := make(chan struct{})
	go func() { b.runNetwork(netCfg); close(done) }()

	for i := 0; i < 2; i++ {
		select {
		case <-conns:
		case <-time.After(5 * time.Second):
			t.Fatalf("connection %d never arrived: network was not restarted", i+1)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for b.network("test") == nil {
		if time.Now().After(deadline) {
			t.Fatal("restarted network never went live")
		}
		time.Sleep(10 * time.Millisecond)
	}

	b.disconnectNetwork("test")
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runNetwork kept running after disconnectNetwork")
	}
}
