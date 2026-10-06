package omdb

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestFetchErrorDoesNotLeakKey(t *testing.T) {
	c := &http.Client{Timeout: 200 * time.Millisecond}
	_, err := FetchByTitle(context.Background(), c, "SECRETKEY", "http://127.0.0.1:1/", "x")
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "SECRETKEY") {
		t.Fatalf("api key leaked: %v", err)
	}
}
