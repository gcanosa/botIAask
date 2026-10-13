package irc

import (
	"encoding/json"
	"testing"
)

func TestLinuxFormats(t *testing.T) {
	var p eolProduct
	json.Unmarshal([]byte(`{"result":{"releases":[
		{"name":"14.5","releaseDate":"2026-09-08"},
		{"name":"27.04","releaseDate":"2027-04-20"},
		{"name":"15.1","releaseDate":"2026-06-16"},
		{"name":"15","releaseDate":"2025-12-02"},
		{"name":"lmde7","releaseDate":"2026-10-01"}]}}`), &p)
	if r, _ := p.newest("2026-10-13"); r.Name != "15.1" {
		t.Errorf("newest: got %q want 15.1", r.Name)
	}

	r := eolRelease{Name: "26.04", Codename: "Resolute Raccoon", IsLts: true}
	r.Latest = &struct {
		Name string `json:"name"`
	}{"26.04.1"}
	if got, want := r.format("Ubuntu"), "Ubuntu \x0226.04.1\x02 LTS (Resolute Raccoon)"; got != want {
		t.Errorf("eol: got %q want %q", got, want)
	}
	if got, want := (eolRelease{Name: "44"}).format("Fedora"), "Fedora \x0244\x02"; got != want {
		t.Errorf("eol no latest: got %q want %q", got, want)
	}

	var l rpiList
	json.Unmarshal([]byte(`{"os_list":[{"name":"Raspberry Pi OS (64-bit)","description":"A port of Debian Trixie with the Raspberry Pi Desktop","release_date":"2026-10-06"}]}`), &l)
	if got, want := l.format(), "Raspberry Pi OS \x022026-10-06\x02 (Trixie)"; got != want {
		t.Errorf("rpi: got %q want %q", got, want)
	}

	var k kernelReleases
	json.Unmarshal([]byte(`{"releases":[{"moniker":"mainline","version":"7.3-rc7"},{"moniker":"stable","version":"7.2.10"},{"moniker":"longterm","version":"6.18.56"},{"moniker":"longterm","version":"6.12.112"},{"moniker":"linux-next","version":"next-20261012"}]}`), &k)
	want := "\x0303,01[KERNEL]\x03 mainline \x027.3-rc7\x02 · stable \x027.2.10\x02 · longterm 6.18.56, 6.12.112 · linux-next next-20261012"
	if got := k.format(); got != want {
		t.Errorf("kernel: got %q want %q", got, want)
	}
}
