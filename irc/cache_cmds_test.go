package irc

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"botIAask/config"
)

func newCmdNetwork(t *testing.T) (*ircNetwork, *sink) {
	t.Helper()
	return newCaptureNetwork(t, &config.Config{})
}

// A second !linux within the TTL is answered from memory: one call is 11 upstream requests.
func TestLinuxAnswerIsCached(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch {
		case strings.Contains(r.URL.Path, "kernel"):
			w.Write([]byte(`{"releases":[{"moniker":"stable","version":"7.2.10"}]}`))
		case strings.Contains(r.URL.Path, "rpi"):
			w.Write([]byte(`{"os_list":[{"name":"Raspberry Pi OS","description":"Debian Trixie","release_date":"2026-10-06"}]}`))
		default:
			w.Write([]byte(`{"result":{"releases":[{"name":"1.0","releaseDate":"2020-01-01"}]}}`))
		}
	}))
	defer srv.Close()
	oldE, oldR, oldK := linuxEOLAPI, linuxRPiList, linuxKernel
	linuxEOLAPI, linuxRPiList, linuxKernel = srv.URL+"/eol/", srv.URL+"/rpi", srv.URL+"/kernel"
	defer func() { linuxEOLAPI, linuxRPiList, linuxKernel = oldE, oldR, oldK }()
	linuxCache = linuxCache0()

	n, out := newCmdNetwork(t)
	n.handleLinuxCommand("#c", "alice")
	first := hits.Load()
	if first < 9 || len(out.all()) != 3 {
		t.Fatalf("first call: %d requests, %d lines", first, len(out.all()))
	}
	n.handleLinuxCommand("#c", "bob")
	if hits.Load() != first {
		t.Fatalf("second call made %d extra upstream requests", hits.Load()-first)
	}
	if len(out.all()) != 6 {
		t.Fatalf("cached answer not sent: %q", out.all())
	}
}

// An answer with a missing source must not be cached, so the next call can recover.
func TestLinuxPartialAnswerIsNotCached(t *testing.T) {
	var kernelUp atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "kernel"):
			if !kernelUp.Load() {
				http.Error(w, "down", 503)
				return
			}
			w.Write([]byte(`{"releases":[{"moniker":"stable","version":"7.2.10"}]}`))
		case strings.Contains(r.URL.Path, "rpi"):
			w.Write([]byte(`{"os_list":[]}`))
		default:
			w.Write([]byte(`{"result":{"releases":[{"name":"1.0","releaseDate":"2020-01-01"}]}}`))
		}
	}))
	defer srv.Close()
	oldE, oldR, oldK := linuxEOLAPI, linuxRPiList, linuxKernel
	linuxEOLAPI, linuxRPiList, linuxKernel = srv.URL+"/eol/", srv.URL+"/rpi", srv.URL+"/kernel"
	defer func() { linuxEOLAPI, linuxRPiList, linuxKernel = oldE, oldR, oldK }()
	linuxCache = linuxCache0()

	n, out := newCmdNetwork(t)
	n.handleLinuxCommand("#c", "alice")
	if got := strings.Join(out.all(), "\n"); strings.Contains(got, "KERNEL") {
		t.Fatalf("kernel line should be missing while the source is down:\n%s", got)
	}
	kernelUp.Store(true)
	n.handleLinuxCommand("#c", "alice")
	if got := strings.Join(out.all(), "\n"); !strings.Contains(got, "KERNEL") {
		t.Fatalf("recovered source never showed up (partial answer was cached):\n%s", got)
	}
}
