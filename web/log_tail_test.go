package web

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "x.log")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadFileTail(t *testing.T) {
	var sb strings.Builder
	for i := 1; i <= 5000; i++ { // ~100 KB: spans several 64 KiB chunks
		fmt.Fprintf(&sb, "[00:00:00] <n> line %05d %s\n", i, strings.Repeat("x", 12))
	}
	p := writeTemp(t, sb.String())

	got, trunc, err := readFileTail(p, 10)
	if err != nil || !trunc || len(got) != 10 || !strings.Contains(got[0], "line 04991") || !strings.Contains(got[9], "line 05000") {
		t.Fatalf("tail 10 = %v trunc=%v err=%v", got, trunc, err)
	}
	got, trunc, _ = readFileTail(p, 40000)
	if trunc || len(got) != 5000 || !strings.Contains(got[0], "line 00001") {
		t.Fatalf("whole file: len=%d trunc=%v first=%q", len(got), trunc, got[0])
	}
}

func TestReadFileTailEdgeCases(t *testing.T) {
	if got, trunc, err := readFileTail(writeTemp(t, ""), 5); err != nil || trunc || len(got) != 0 {
		t.Fatalf("empty file: %v %v %v", got, trunc, err)
	}
	// unterminated last line is still returned (history shows it), and is reported as partial
	p := writeTemp(t, "a\nb\nc")
	got, _, _ := readFileTail(p, 10)
	if strings.Join(got, ",") != "a,b,c" {
		t.Fatalf("got %v", got)
	}
	f, _ := os.Open(p)
	defer f.Close()
	lines, partial, trunc, _ := tailLines(f, 5, 10)
	if strings.Join(lines, ",") != "a,b" || partial != "c" || trunc {
		t.Fatalf("tailLines = %v %q %v", lines, partial, trunc)
	}
	// cutting at an arbitrary size mid-line leaves the unfinished line as partial
	lines, partial, _, _ = tailLines(f, 4, 10)
	if strings.Join(lines, ",") != "a,b" || partial != "" {
		t.Fatalf("size=4: %v %q", lines, partial)
	}
}
