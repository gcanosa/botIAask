package irc

import (
	"sync"
	"testing"

	"botIAask/config"
)

// TestAIRequestsCounter_CrossNetworkRace guards a regression of the statsMu mutex-identity
// bug: incrementing aiRequests (a *Bot field) must lock Bot.statsMu explicitly. Doing it via
// the promoted b.statsMu from an *ircNetwork receiver silently locks ircNetwork.statsMu (an
// unrelated per-connection mutex) instead — this test drives that increment concurrently
// from two different ircNetwork instances plus concurrent reads via GetAIRequestCount, so
// `go test -race` catches a regression back to the wrong lock.
func TestAIRequestsCounter_CrossNetworkRace(t *testing.T) {
	b := newTestBot(&config.Config{
		IRC: config.IRCConfig{Networks: []config.IRCNetworkConfig{{Name: "alpha"}, {Name: "beta"}}},
	})
	alpha := &ircNetwork{Bot: b, name: "alpha", channelMembers: make(map[string]map[string]struct{})}
	beta := &ircNetwork{Bot: b, name: "beta", channelMembers: make(map[string]map[string]struct{})}

	const perNetwork = 200
	var wg sync.WaitGroup
	increment := func(n *ircNetwork) {
		defer wg.Done()
		for i := 0; i < perNetwork; i++ {
			// Same statement shape as the fixed call site (irc/bot.go, !ask handler).
			n.Bot.statsMu.Lock()
			n.aiRequests++
			n.Bot.statsMu.Unlock()
		}
	}
	wg.Add(3)
	go increment(alpha)
	go increment(beta)
	go func() {
		defer wg.Done()
		for i := 0; i < perNetwork; i++ {
			_ = b.GetAIRequestCount()
		}
	}()
	wg.Wait()

	if got := b.GetAIRequestCount(); got != 2*perNetwork {
		t.Fatalf("aiRequests = %d, want %d (lost updates indicate the wrong mutex is held)", got, 2*perNetwork)
	}
}
