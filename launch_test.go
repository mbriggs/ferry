package main

import (
	"os"
	"reflect"
	"testing"
)

func TestLaunchPrefix(t *testing.T) {
	l := &Local{Home: t.TempDir()}
	if got := l.launchPrefix(); got != nil {
		t.Fatalf("no launch file: prefix %q, want none", got)
	}
	os.MkdirAll(l.configDir(), 0o755)
	os.WriteFile(l.configDir("launch"), []byte("# run agents in their worktree's box\n\n  bunk run  \nignored\n"), 0o644)
	if got, want := l.launchPrefix(), []string{"bunk", "run"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("prefix %q, want %q", got, want)
	}
}
