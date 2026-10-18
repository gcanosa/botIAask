package config

import "testing"

func TestMaskMatch(t *testing.T) {
	for _, c := range []struct {
		mask, src string
		want      bool
	}{
		{"*!*@evil.host", "Bob!~b@EVIL.host", true},
		{"*!*@*.evil.host", "x!y@a.b.evil.host", true},
		{"*!*@*.evil.host", "x!y@evil.host", false},
		{"bob!*@*", "bobby!u@h", false},
		{"b?b!*@*", "bib!u@h", true},
		{"*!*@user/bob", "n!u@user/bob", true}, // cloaks contain '/', which path.Match would mishandle
		{"*", "anything!u@h", true},
		{"a*b*c", "axxbyyc", true},
		{"a*b*c", "axxbyy", false},
	} {
		if got := MaskMatch(c.mask, c.src); got != c.want {
			t.Errorf("MaskMatch(%q, %q) = %v", c.mask, c.src, got)
		}
	}
}

func TestNormalizeAndValidateAccess(t *testing.T) {
	for in, want := range map[string]string{"bob": "bob!*@*", "*@1.2.3.4": "*!*@1.2.3.4", "bob!x": "bob!x@*", "a!b@c": "a!b@c"} {
		if got := NormalizeMask(in); got != want {
			t.Errorf("NormalizeMask(%q) = %q, want %q", in, got, want)
		}
	}
	e := ChannelAccess{Channel: "#c", Mask: "*", Level: "KICK"}
	if ValidateAccess(&e) == nil {
		t.Error("kick-everyone mask accepted")
	}
	e = ChannelAccess{Channel: "c", Mask: "bob", Level: "op"}
	if ValidateAccess(&e) == nil {
		t.Error("channel without # accepted")
	}
	e = ChannelAccess{Channel: "#c", Mask: " bob ", Level: "Voice"}
	if err := ValidateAccess(&e); err != nil || e.Mask != "bob!*@*" || e.Level != "voice" {
		t.Errorf("got %+v, %v", e, err)
	}
}
