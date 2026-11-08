package main

import "testing"

func TestRsyncVersionGate(t *testing.T) {
	for v, ok := range map[string]bool{
		"":       false, // missing, or openrsync
		"2.6.9":  false,
		"3.2.4":  false,
		"3.2.5":  true,
		"3.4.4":  true,
		"3.10.0": true,
		"4.0.0":  true,
	} {
		if err := checkRsync("m", v); (err == nil) != ok {
			t.Errorf("checkRsync(%q) = %v, want ok=%v", v, err, ok)
		}
	}
	for out, want := range map[string]string{
		"rsync  version 3.4.4  protocol version 32\n":         "3.4.4",
		"rsync  version v3.2.7  protocol version 31\n":        "3.2.7",
		"openrsync: protocol version 29\nrsync version 2.6.9": "",
	} {
		m := rsyncVersionRE.FindStringSubmatch(out)
		got := ""
		if m != nil {
			got = m[1] + "." + m[2] + "." + m[3]
		}
		if got != want {
			t.Errorf("parsing %q: got %q, want %q", out, got, want)
		}
	}
}
