package irc

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ergochat/irc-go/ircutils"
)

func TestSplitUTF8ByByteBudget(t *testing.T) {
	t.Parallel()
	long := strings.Join(make([]string, 80), "word ") + "word"
	parts := splitUTF8ByByteBudget(long, 100)
	for i, p := range parts {
		if len([]byte(p)) > 100 {
			t.Fatalf("part %d length %d > 100", i, len([]byte(p)))
		}
	}
	joined := strings.Join(parts, " ")
	if joined != long {
		t.Fatalf("round-trip: got %q want %q", joined, long)
	}
}

func TestSplitUTF8ByByteBudget_UTF8(t *testing.T) {
	t.Parallel()
	s := strings.Repeat("é", 300) // 2 bytes per rune
	parts := splitUTF8ByByteBudget(s, 50)
	for i, p := range parts {
		if !utf8.ValidString(p) {
			t.Fatalf("part %d invalid utf8", i)
		}
		if len([]byte(p)) > 50 {
			t.Fatalf("part %d too long", i)
		}
	}
}

func TestHelpLinesFitSanitizer(t *testing.T) {
	t.Parallel()
	public := strings.Repeat("a", 500)
	admin := strings.Repeat("b", 500)
	for _, chunk := range splitUTF8ByByteBudget(public, 400) {
		line := "@tester: " + chunk
		out := ircutils.SanitizeText(line, ircTextBudget)
		if len([]byte(out)) > ircTextBudget {
			t.Fatalf("sanitized line still over budget: %d", len([]byte(out)))
		}
	}
	for _, chunk := range splitUTF8ByByteBudget(admin, 400) {
		line := "@tester: " + chunk
		out := ircutils.SanitizeText(line, ircTextBudget)
		if len([]byte(out)) > ircTextBudget {
			t.Fatalf("sanitized line still over budget: %d", len([]byte(out)))
		}
	}
}
