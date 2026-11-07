package main

import (
	"os"
	"testing"
	"time"
)

func TestHashCacheSkipsUnchangedFiles(t *testing.T) {
	l := &Local{Home: t.TempDir()}
	p := l.abs("dump")
	os.WriteFile(p, []byte("v1"), 0o600)
	real, _ := fileSHA(p)

	c := l.hashCache()
	if got, _ := c.sha(p); got != real {
		t.Fatal("first hash is wrong")
	}
	st, _ := os.Lstat(p)
	c.put(p, st, "cached-not-recomputed")
	c.save()

	c = l.hashCache()
	if got, _ := c.sha(p); got != "cached-not-recomputed" {
		t.Fatalf("an unchanged file was hashed again: %s", got)
	}
	// Same size, new mtime: hashed afresh.
	os.WriteFile(p, []byte("v2"), 0o600)
	later := time.Now().Add(time.Second)
	os.Chtimes(p, later, later)
	want, _ := fileSHA(p)
	if got, _ := c.sha(p); got != want {
		t.Fatalf("a changed file came from the cache: %s", got)
	}
}
