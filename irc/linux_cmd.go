package irc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"botIAask/internal/guard"
	"botIAask/meta"
)

// Endpoints (vars so tests can point them at httptest servers).
var (
	linuxEOLAPI  = "https://endoflife.date/api/v1/products/"
	linuxRPiList = "https://downloads.raspberrypi.com/os_list_imagingutility_v4.json"
	linuxKernel  = "https://www.kernel.org/releases.json"
)

// linuxDistros is the display order: label and endoflife.date product id.
// ponytail: classic CentOS is EOL (8, 2021), so CentOS Stream stands in for it.
var linuxDistros = []struct{ label, product string }{
	{"Ubuntu", "ubuntu"}, {"Mint", "linuxmint"}, {"Debian", "debian"},
	{"RHEL", "rhel"}, {"CentOS Stream", "centos-stream"}, {"Fedora", "fedora"},
}

var bsdDistros = []struct{ label, product string }{
	{"FreeBSD", "freebsd"}, {"NetBSD", "netbsd"}, {"OpenBSD", "openbsd"},
}

func linuxGetJSON(ctx context.Context, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", meta.Name+"/"+meta.Version+" (IRC bot)")
	resp, err := latestHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// eolRelease is the subset of an endoflife.date release cycle we use.
type eolRelease struct {
	Name        string `json:"name"`
	Codename    string `json:"codename"`
	IsLts       bool   `json:"isLts"`
	ReleaseDate string `json:"releaseDate"`
	Latest      *struct {
		Name string `json:"name"`
	} `json:"latest"`
}

type eolProduct struct {
	Result struct {
		Releases []eolRelease `json:"releases"`
	} `json:"result"`
}

// newest picks the highest released cycle by version number. Not the API's "latest", which is
// the most recently *dated* cycle (FreeBSD 14.5 shipped after 15.1). Unreleased cycles are skipped.
func (p eolProduct) newest(today string) (best eolRelease, ok bool) {
	for _, r := range p.Result.Releases {
		if r.ReleaseDate > today || (ok && !versionLess(best.Name, r.Name)) {
			continue
		}
		best, ok = r, true
	}
	return best, ok
}

// format renders "Ubuntu 26.04.1 LTS (Resolute Raccoon)"; latest patch release when known, else the cycle.
func (r eolRelease) format(label string) string {
	v := r.Name
	if r.Latest != nil && r.Latest.Name != "" {
		v = r.Latest.Name
	}
	s := label + " " + ircBold + v + ircBold
	if r.IsLts {
		s += " LTS"
	}
	if r.Codename != "" {
		s += " (" + r.Codename + ")"
	}
	return s
}

var rpiDebianRe = regexp.MustCompile(`Debian (\w+)`)

type rpiList struct {
	OSList []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		ReleaseDate string `json:"release_date"`
	} `json:"os_list"`
}

// format renders "Raspberry Pi OS 2026-10-06 (Trixie)" from the recommended (first) image.
func (l rpiList) format() string {
	for _, o := range l.OSList {
		if strings.HasPrefix(o.Name, "Raspberry Pi OS") && o.ReleaseDate != "" {
			s := "Raspberry Pi OS " + ircBold + o.ReleaseDate + ircBold
			if m := rpiDebianRe.FindStringSubmatch(o.Description); m != nil {
				s += " (" + m[1] + ")"
			}
			return s
		}
	}
	return ""
}

type kernelReleases struct {
	Releases []struct {
		Moniker string `json:"moniker"`
		Version string `json:"version"`
	} `json:"releases"`
}

// format renders "[KERNEL] mainline 7.3-rc7 · stable 7.2.10 · longterm 6.18.56, 6.12.112 · linux-next next-…".
func (k kernelReleases) format() string {
	var order []string
	byMoniker := map[string][]string{}
	for _, r := range k.Releases {
		if _, ok := byMoniker[r.Moniker]; !ok {
			order = append(order, r.Moniker)
		}
		byMoniker[r.Moniker] = append(byMoniker[r.Moniker], r.Version)
	}
	parts := make([]string, 0, len(order))
	for i, m := range order {
		v := byMoniker[m]
		if i < 2 { // mainline and stable lead: make them stand out
			v[0] = ircBold + v[0] + ircBold
		}
		parts = append(parts, m+" "+strings.Join(v, ", "))
	}
	return "\x0303,01[KERNEL]\x03 " + strings.Join(parts, " · ")
}

// fetchEOL fills out[slot(i)] with each product's newest release; failures stay "".
func fetchEOL(ctx context.Context, wg *sync.WaitGroup, list []struct{ label, product string }, out []string, slot func(int) int) {
	today := time.Now().UTC().Format("2006-01-02")
	for i, d := range list {
		wg.Add(1)
		guard.Go("linux eol", func() {
			defer wg.Done()
			var p eolProduct
			if linuxGetJSON(ctx, linuxEOLAPI+d.product, &p) == nil {
				if r, ok := p.newest(today); ok {
					out[slot(i)] = r.format(d.label)
				}
			}
		})
	}
}

func tagLine(tag string, parts []string) string {
	var ok []string
	for _, p := range parts {
		if p != "" {
			ok = append(ok, p)
		}
	}
	if len(ok) == 0 {
		return ""
	}
	return "\x0303,01[" + tag + "]\x03 " + strings.Join(ok, " · ")
}

// fetchLinux returns the [LINUX], [BSD] and [KERNEL] lines; a failed source is just left out.
func fetchLinux(ctx context.Context) (distros, bsd, kernel string) {
	parts := make([]string, len(linuxDistros)+1) // +1: Raspberry Pi OS, right after Debian (index 2)
	bsds := make([]string, len(bsdDistros))
	var wg sync.WaitGroup
	fetchEOL(ctx, &wg, linuxDistros, parts, func(i int) int {
		if i > 2 {
			return i + 1
		}
		return i
	})
	fetchEOL(ctx, &wg, bsdDistros, bsds, func(i int) int { return i })
	wg.Add(2)
	guard.Go("linux rpi", func() {
		defer wg.Done()
		var l rpiList
		if linuxGetJSON(ctx, linuxRPiList, &l) == nil {
			parts[3] = l.format()
		}
	})
	guard.Go("linux kernel", func() {
		defer wg.Done()
		var k kernelReleases
		if linuxGetJSON(ctx, linuxKernel, &k) == nil && len(k.Releases) > 0 {
			kernel = k.format()
		}
	})
	wg.Wait()
	return tagLine("LINUX", parts), tagLine("BSD", bsds), kernel
}

// handleLinuxCommand reports the newest release of popular distros, the kernel.org trees and the BSDs (!linux).
func (b *ircNetwork) handleLinuxCommand(target, sender string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	distros, bsd, kernel := fetchLinux(ctx)
	if distros == "" && bsd == "" && kernel == "" {
		b.sendPrivmsg(target, b.sanitize(fmt.Sprintf("@%s: linux: release sources unreachable", sender)))
		return
	}
	for _, l := range []string{distros, kernel, bsd} {
		if l != "" {
			b.sendPrivmsg(target, b.sanitize(l))
		}
	}
}
