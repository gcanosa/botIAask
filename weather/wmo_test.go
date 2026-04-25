package weather

import "testing"

func TestIconKind(t *testing.T) {
	if IconKind(0) != "clear" {
		t.Fatalf("0 -> clear, got %s", IconKind(0))
	}
	if IconKind(95) != "thunder" {
		t.Fatalf("95 -> thunder, got %s", IconKind(95))
	}
}
