package logger

import "testing"

// Two networks sharing a channel name must not collide onto the same log file key.
func TestChannelFileKeyDistinctPerNetwork(t *testing.T) {
	a := ChannelFileKey("#general", "libera")
	b := ChannelFileKey("#general", "oftc")
	if a == b {
		t.Fatalf("expected distinct keys for different networks, got %q for both", a)
	}
	if a != "libera_general" {
		t.Errorf("got %q, want %q", a, "libera_general")
	}
	if b != "oftc_general" {
		t.Errorf("got %q, want %q", b, "oftc_general")
	}
}

func TestChannelFileKeyPMFallsBackToServerName(t *testing.T) {
	if got := ChannelFileKey("somenick", "libera"); got != "libera" {
		t.Errorf("got %q, want %q", got, "libera")
	}
}
