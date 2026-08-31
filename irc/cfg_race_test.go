package irc

import (
	"fmt"
	"sync"
	"testing"

	"botIAask/config"
)

// TestApplyLiveConfigRace hammers GetConfig/IsAdmin/pfx/cmd from many goroutines
// while ApplyLiveConfig repeatedly swaps the config pointer. Run with -race:
// the atomic.Pointer swap in ApplyLiveConfig must make every read safe even
// though the bot is never connected (so the channel-sync path returns early).
// The network's Name/Server/Port/Nickname/UseSSL/Services are kept identical across
// every swap on purpose: a real endpoint change makes ApplyLiveConfig spawn a live
// reconnect goroutine (see network.go), which this test must not trigger.
func TestApplyLiveConfigRace(t *testing.T) {
	b := newTestBot(&config.Config{
		IRC:   config.IRCConfig{Networks: []config.IRCNetworkConfig{{Name: "test", Nickname: "TestBot"}}},
		Bot:   config.BotConfig{CommandPrefix: "!", CommandName: "ask"},
		Admin: config.AdminConfig{Admins: []string{"admin!user@host"}},
	})

	var wg sync.WaitGroup
	stop := make(chan struct{})

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = b.GetConfig()
				_ = b.IsAdmin("admin!user@host")
				_ = b.pfx()
				_ = b.cmd()
				_ = b.limiter()
			}
		}()
	}

	for i := 0; i < 200; i++ {
		b.ApplyLiveConfig(&config.Config{
			IRC:   config.IRCConfig{Networks: []config.IRCNetworkConfig{{Name: "test", Nickname: "TestBot"}}},
			Bot:   config.BotConfig{CommandPrefix: "!", CommandName: fmt.Sprintf("ask%d", i)},
			Admin: config.AdminConfig{Admins: []string{"admin!user@host"}},
		})
	}

	close(stop)
	wg.Wait()
}
