package ircfold

import "testing"

func TestNick(t *testing.T) {
	for in, want := range map[string]string{
		"Alice": "alice", "Al[ice]": "al{ice}", `a\b^c`: "a|b~c", "ünï": "ünï", "": "",
	} {
		if got := Nick(in); got != want {
			t.Errorf("Nick(%q) = %q, want %q", in, got, want)
		}
	}
}
