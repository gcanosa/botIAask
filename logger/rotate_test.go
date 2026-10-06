package logger

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestCompressAndMoveLog(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "a.log")
	dst := filepath.Join(d, "a.log.gz")
	os.WriteFile(src, []byte("hello\n"), 0644)
	if err := compressAndMoveLog(src, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("source should be removed after a good archive")
	}
	f, _ := os.Open(dst)
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(gz)
	if string(b) != "hello\n" {
		t.Fatalf("archive content = %q", b)
	}
}

func TestCompressAndMoveLogKeepsSourceOnFailure(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "a.log")
	os.WriteFile(src, []byte("keep\n"), 0644)
	// destination in a missing directory → Create fails → source must survive
	if err := compressAndMoveLog(src, filepath.Join(d, "nope", "a.gz")); err == nil {
		t.Fatal("expected error")
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatal("source must be kept when archiving fails")
	}
}
