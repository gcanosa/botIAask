package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"botIAask/config"
	"botIAask/meta"
)

const (
	ansiReset = "\033[0m"
	ansiGreen = "\033[32m"
	ansiRed   = "\033[31m"
	ansiDim   = "\033[2m"
)

const colService = 34

func stdoutSupportsColor() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	return term.IsTerminal(int(os.Stdout.Fd()))
}

func printAppIdentity(w io.Writer, color bool) {
	dim := ansiDim
	reset := ansiReset
	if !color {
		dim, reset = "", ""
	}
	fmt.Fprintf(w, "%s\n", meta.Name)
	fmt.Fprintf(w, "%sv%s%s\n", dim, meta.Version, reset)
	fmt.Fprintf(w, "%s\n", meta.Author)
}

func paint(color bool, code, s string) string {
	if !color {
		return s
	}
	return code + s + ansiReset
}

func rssPrimaryFeedHint(cfg *config.Config) string {
	if len(cfg.RSS.FeedURLs) > 0 {
		u := strings.TrimSpace(cfg.RSS.FeedURLs[0])
		if u != "" {
			return u
		}
	}
	return "https://news.ycombinator.com/rss"
}

func printDaemonParentReport(w io.Writer, cfg *config.Config, configPath string, color bool) {
	good := func(s string) string { return paint(color, ansiGreen, s) }
	bad := func(s string) string { return paint(color, ansiRed, s) }
	dim := func(s string) string { return paint(color, ansiDim, s) }

	fmt.Fprintf(w, "\n%s\n", dim("── Process ──"))
	printRow(w, "Config file", configPath, "")
	printRow(w, "PID file", cfg.Daemon.PIDFile, "")

	fmt.Fprintf(w, "\n%s\n", dim("── Core ──"))
	ircTarget := fmt.Sprintf("%s:%d (SSL %v)", cfg.IRC.Server, cfg.IRC.Port, cfg.IRC.UseSSL)
	printRow(w, "IRC server", ircTarget, "")
	printRow(w, "IRC nickname", cfg.IRC.Nickname, "")
	printRow(w, "IRC bot", "", good("OK"))
	fmt.Fprintf(w, "  %-*s %s\n", colService, "LM Studio URL", cfg.AI.LMStudioURL)
	fmt.Fprintf(w, "  %-*s %s\n", colService, "AI model", cfg.AI.Model)

	fmt.Fprintf(w, "\n%s\n", dim("── Web dashboard ──"))
	if cfg.Web.Enabled {
		addr := fmt.Sprintf("%s:%d", cfg.Web.Host, cfg.Web.Port)
		printRow(w, "HTTP listen", addr, good("OK"))
		fmt.Fprintf(w, "  %-*s %s\n", colService, "URL", "http://"+addr)
	} else {
		printRow(w, "HTTP server", "", bad("NOT STARTED"))
	}

	fmt.Fprintf(w, "\n%s\n", dim("── RSS ──"))
	if cfg.RSS.Enabled {
		feed := rssPrimaryFeedHint(cfg)
		detail := fmt.Sprintf("every %d min · %s", cfg.RSS.IntervalMinutes, feed)
		printRow(w, "RSS fetcher", detail, good("OK"))
		if cfg.RSS.AnnounceToIRCEnabled() {
			printRow(w, "RSS → IRC", "announcements on", good("OK"))
		} else {
			printRow(w, "RSS → IRC", "fetch only (no IRC posts)", dim("off"))
		}
	} else {
		printRow(w, "RSS fetcher", "", bad("NOT STARTED"))
	}

	fmt.Fprintf(w, "\n%s\n", dim("── Stats ──"))
	if cfg.Stats.Enabled {
		printRow(w, "Stats tracker", fmt.Sprintf("interval %ds", cfg.Stats.Interval), good("ON"))
	} else {
		printRow(w, "Stats tracker", "", bad("OFF"))
	}

	fmt.Fprintf(w, "\n%s\n", dim("── Logging ──"))
	if cfg.Logger.RotationDays > 0 {
		printRow(w, "Log rotation", fmt.Sprintf("%d day(s)", cfg.Logger.RotationDays), good("ON"))
	} else {
		printRow(w, "Log rotation", "", bad("OFF"))
	}
	fmt.Fprintln(w)
}

func printRow(w io.Writer, service, detail, status string) {
	switch {
	case status != "" && detail != "":
		fmt.Fprintf(w, "  %-*s %s  %s\n", colService, service, detail, status)
	case status != "":
		fmt.Fprintf(w, "  %-*s %s\n", colService, service, status)
	case detail != "":
		fmt.Fprintf(w, "  %-*s %s\n", colService, service, detail)
	default:
		fmt.Fprintf(w, "  %s\n", service)
	}
}
