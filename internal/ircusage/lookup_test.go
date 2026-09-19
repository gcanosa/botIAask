package ircusage

import (
	"sort"
	"testing"
)

func TestLookupAndSorted(t *testing.T) {
	for _, admin := range []bool{false, true} {
		if n := Names(admin); !sort.StringsAreSorted(n) {
			t.Fatalf("names not sorted: %v", n)
		}
	}
	rows, intro, ok := Lookup("!GH")
	if !ok || len(rows) != 4 || intro == "" {
		t.Fatalf("gh: %v %q %v", rows, intro, ok)
	}
	if _, _, ok := Lookup("nope"); ok {
		t.Fatal("unknown command found")
	}
}
