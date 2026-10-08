package irc

import "testing"

func TestBotFlagChange(t *testing.T) {
	for _, c := range []struct {
		in     string
		on, ok bool
	}{{"+B", true, true}, {"-B", false, true}, {"+iwB", true, true}, {"-B+w", false, true}, {"+iw", false, false}} {
		if on, ok := botFlagChange(c.in); on != c.on || ok != c.ok {
			t.Errorf("%q: got %v,%v want %v,%v", c.in, on, ok, c.on, c.ok)
		}
	}
}
