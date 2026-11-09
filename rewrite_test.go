package main

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRewriteHome(t *testing.T) {
	cases := []struct {
		name, in, want string
		n              int
	}{
		{"exact", `{"cwd":"/Users/matt"}`, `{"cwd":"/home/mbriggs"}`, 1},
		{"prefix", `{"cwd":"/Users/matt/src/app"}`, `{"cwd":"/home/mbriggs/src/app"}`, 1},
		{"sibling dir is not a prefix", `{"cwd":"/Users/matthew/src"}`, `{"cwd":"/Users/matthew/src"}`, 0},
		{"free text keeps the old path", `{"cmd":"cd /Users/matt/src && ls"}`, `{"cmd":"cd /Users/matt/src && ls"}`, 0},
		{"nested and in arrays", `{"a":{"b":["/Users/matt/x",1,"y"]}}`, `{"a":{"b":["/home/mbriggs/x",1,"y"]}}`, 1},
		{"keys too", `{"/Users/matt/src":{"trusted":true}}`, `{"/home/mbriggs/src":{"trusted":true}}`, 1},
		{"escaped quotes elsewhere survive", `{"t":"say \"hi\"","p":"/Users/matt/a"}`, `{"t":"say \"hi\"","p":"/home/mbriggs/a"}`, 1},
		{"numbers and spacing untouched", `{"n": 12345678901234567890, "p" : "/Users/matt"}`, `{"n": 12345678901234567890, "p" : "/home/mbriggs"}`, 1},
		{"html characters not escaped", `{"p":"/Users/matt/a<b>&c"}`, `{"p":"/home/mbriggs/a<b>&c"}`, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, n, err := rewriteHome([]byte(c.in), "/Users/matt", "/home/mbriggs")
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != c.want || n != c.n {
				t.Fatalf("got %s (%d), want %s (%d)", got, n, c.want, c.n)
			}
		})
	}
}

func TestRewriteHomeLinesRejectsBadJSON(t *testing.T) {
	_, _, err := rewriteHomeLines([]byte("{\"ok\":1}\n{broken\n"), "/a", "/b")
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("want a line 2 error, got %v", err)
	}
}

// FuzzRewriteHome checks, for any pair of homes, that valid JSON stays
// valid, that every string under the old home and nothing else changes, and
// that rewriting back restores the document, key order and number spellings
// included. The round trip only holds when nothing was under the new home
// already, so those documents are skipped.
func FuzzRewriteHome(f *testing.F) {
	const matt, fresh = "/Users/matt", "/zz-fresh"
	f.Add(`{"cwd":"/Users/matt/src","x":["/Users/matt",2]}`, matt, fresh)
	f.Add(`"/Users/matt"`, matt, fresh)
	f.Add(`{"p":"/home/m/x","q":"/home/mbriggs/y"}`, "/home/m", "/home/mbriggs")
	f.Add(`{"p":"/home/mbriggs/x","n":1.50}`, "/home/mbriggs", "/home/m")
	f.Add(`["/a b/\"q\"/c"]`, `/a b/"q"`, "/Users/<x>&é")
	f.Add(`[{"a":"é\"/Users/matt/x"}]`, matt, fresh)
	f.Fuzz(func(t *testing.T, in, oldHome, newHome string) {
		if !isHome(oldHome) || !isHome(newHome) {
			return
		}
		doc, err := decodeOrdered([]byte(in))
		if err != nil || countUnder(doc, newHome) > 0 {
			return
		}
		out, n, err := rewriteHome([]byte(in), oldHome, newHome)
		if err != nil {
			t.Fatal(err)
		}
		if !json.Valid(out) {
			t.Fatalf("invalid output %q", out)
		}
		if want := countUnder(doc, oldHome); n != want {
			t.Fatalf("rewrote %d strings, want %d:\n%s\n%s", n, want, in, out)
		}
		back, _, err := rewriteHome(out, newHome, oldHome)
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := decodeOrdered(back); !reflect.DeepEqual(doc, got) {
			t.Fatalf("round trip changed the document:\n%s\n%s", in, back)
		}
	})
}

// isHome reports whether h could be a home directory: absolute, clean, not
// the root, and valid UTF-8, since that's all the OS hands out.
func isHome(h string) bool {
	return filepath.IsAbs(h) && filepath.Clean(h) == h && h != "/" && utf8.ValidString(h)
}

// countUnder counts the strings in a decoded document, keys included, that
// are home or a path under it.
func countUnder(v any, home string) int {
	switch x := v.(type) {
	case string:
		if x == home || strings.HasPrefix(x, home+"/") {
			return 1
		}
	case []any:
		n := 0
		for _, e := range x {
			n += countUnder(e, home)
		}
		return n
	case *ordered:
		n := 0
		for _, k := range x.keys {
			n += countUnder(k, home) + countUnder(x.vals[k], home)
		}
		return n
	}
	return 0
}
