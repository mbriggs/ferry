package main

import (
	"encoding/json"
	"strings"
	"testing"
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

// FuzzRewriteHome checks that any valid JSON stays valid, and that rewriting
// to a fresh home and back restores the original document.
func FuzzRewriteHome(f *testing.F) {
	f.Add(`{"cwd":"/Users/matt/src","x":["/Users/matt",2]}`)
	f.Add(`"/Users/matt"`)
	f.Add(`[{"a":"é\"/Users/matt/x"}]`)
	f.Fuzz(func(t *testing.T, in string) {
		if !json.Valid([]byte(in)) || strings.Contains(in, "/zz-fresh") {
			return
		}
		out, _, err := rewriteHome([]byte(in), "/Users/matt", "/zz-fresh")
		if err != nil {
			t.Fatal(err)
		}
		if !json.Valid(out) {
			t.Fatalf("invalid output %q", out)
		}
		back, _, err := rewriteHome(out, "/zz-fresh", "/Users/matt")
		if err != nil {
			t.Fatal(err)
		}
		var a, b any
		json.Unmarshal([]byte(in), &a)
		json.Unmarshal(back, &b)
		ja, _ := json.Marshal(a)
		jb, _ := json.Marshal(b)
		if string(ja) != string(jb) {
			t.Fatalf("round trip changed the document:\n%s\n%s", ja, jb)
		}
	})
}
