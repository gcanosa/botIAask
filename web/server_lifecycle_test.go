package web

import (
	"context"
	"testing"
)

func TestServer_StopIdempotent(t *testing.T) {
	s := &Server{}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatalf("first stop: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2e9) // 2s
	defer cancel()
	if err := s.Stop(ctx); err != nil {
		t.Fatalf("second stop: %v", err)
	}
}
