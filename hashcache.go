package main

import (
	"encoding/json"
	"io/fs"
	"os"
)

// hashCache remembers the sha256 of files ferry has hashed or installed,
// keyed by path, size, and modification time, so an unchanged multi-gigabyte
// dump isn't read again on every handoff. A file whose size or mtime changed
// is hashed afresh. Each machine keeps its own cache; nothing in it ever
// crosses to the other side.
type hashCache struct {
	path    string
	entries map[string]cacheEntry
	dirty   bool
}

type cacheEntry struct {
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"` // nanoseconds
	SHA   string `json:"sha"`
}

func (l *Local) hashCache() *hashCache {
	c := &hashCache{path: l.stateDir("hashes.json"), entries: map[string]cacheEntry{}}
	if data, err := os.ReadFile(c.path); err == nil {
		json.Unmarshal(data, &c.entries)
	}
	return c
}

// sha returns the sha256 of the regular file at p, from the cache when its
// size and mtime are unchanged.
func (c *hashCache) sha(p string) (string, error) {
	st, err := os.Lstat(p)
	if err != nil {
		return "", err
	}
	if e, ok := c.entries[p]; ok && e.Size == st.Size() && e.MTime == st.ModTime().UnixNano() {
		return e.SHA, nil
	}
	sum, err := fileSHA(p)
	if err != nil {
		return "", err
	}
	c.put(p, st, sum)
	return sum, nil
}

// put records a sha already known for p, such as a file just installed
// after its transfer was checked.
func (c *hashCache) put(p string, st fs.FileInfo, sum string) {
	c.entries[p] = cacheEntry{Size: st.Size(), MTime: st.ModTime().UnixNano(), SHA: sum}
	c.dirty = true
}

// save writes the cache, dropping entries for files that no longer exist.
func (c *hashCache) save() {
	if !c.dirty {
		return
	}
	for p := range c.entries {
		if _, err := os.Lstat(p); err != nil {
			delete(c.entries, p)
		}
	}
	if data, err := json.Marshal(c.entries); err == nil {
		writeAtomic(c.path, data, 0o600)
	}
}
