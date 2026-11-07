package main

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type FileEntry struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// defaultInclude is used when a worktree has no .ferry/include.
var defaultInclude = []string{".env*"}

// ListExtras returns the ignored files in a worktree that should travel with
// it: those matching .ferry/include (by base name or path), plus everything
// under .ferry/state. Tracked and untracked files already ride in the git
// snapshot, so only ignored files are considered.
func (l *Local) ListExtras(wtRel string) ([]FileEntry, error) {
	root := l.abs(wtRel)
	patterns := defaultInclude
	if data, err := os.ReadFile(filepath.Join(root, ".ferry", "include")); err == nil {
		patterns = nil
		for _, line := range strings.Split(string(data), "\n") {
			if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
				patterns = append(patterns, line)
			}
		}
	}
	out, err := git(root, "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var entries []FileEntry
	add := func(rel string) {
		if seen[rel] {
			return
		}
		if st, err := os.Lstat(filepath.Join(root, rel)); err == nil && st.Mode().IsRegular() {
			seen[rel] = true
			entries = append(entries, FileEntry{Path: rel, Size: st.Size()})
		}
	}
	for _, rel := range strings.Split(out, "\x00") {
		if rel == "" {
			continue
		}
		if strings.HasSuffix(rel, "/") {
			dir := strings.TrimSuffix(rel, "/")
			if dir == stateDirRel || strings.HasPrefix(dir, stateDirRel+"/") || strings.HasPrefix(stateDirRel, dir+"/") {
				walkFiles(filepath.Join(root, dir), func(p string) {
					r, _ := filepath.Rel(root, p)
					if r == stateDirRel || strings.HasPrefix(r, stateDirRel+string(filepath.Separator)) || matchAny(patterns, r) {
						add(r)
					}
				})
			}
			continue
		}
		if strings.HasPrefix(rel, stateDirRel+"/") || matchAny(patterns, rel) {
			add(rel)
		}
	}
	// .ferry/state may be untracked but not ignored; it's excluded from the
	// snapshot either way, so it always travels as an extra.
	walkFiles(filepath.Join(root, stateDirRel), func(p string) {
		r, _ := filepath.Rel(root, p)
		add(r)
	})
	return entries, nil
}

func matchAny(patterns []string, rel string) bool {
	for _, p := range patterns {
		if ok, _ := filepath.Match(p, filepath.Base(rel)); ok {
			return true
		}
		if ok, _ := filepath.Match(p, rel); ok {
			return true
		}
	}
	return false
}

func walkFiles(dir string, fn func(string)) {
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			fn(p)
		}
		return nil
	})
}

type ExportReq struct {
	BaseRel string   `json:"base_rel"`
	Paths   []string `json:"paths"`
}

// Export writes a tar of the given paths (files or whole directories),
// relative to BaseRel, to w.
func (l *Local) Export(q ExportReq, w io.Writer) error {
	base := l.abs(q.BaseRel)
	tw := tar.NewWriter(w)
	for _, p := range q.Paths {
		err := filepath.WalkDir(filepath.Join(base, p), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.Type().IsRegular() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(base, path)
			hdr := &tar.Header{Name: filepath.ToSlash(rel), Mode: int64(info.Mode().Perm()), Size: info.Size(), ModTime: info.ModTime(), Typeflag: tar.TypeReg}
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.Copy(tw, f)
			return err
		})
		if err != nil {
			return err
		}
	}
	return tw.Close()
}

type ImportReq struct {
	BaseRel string `json:"base_rel"`
	Clean   bool   `json:"clean"`   // empty BaseRel first (staging directories)
	Private bool   `json:"private"` // write files 0600 (secrets)
}

// Import extracts a tar from r under BaseRel. Entries that would land outside
// BaseRel are rejected.
func (l *Local) Import(q ImportReq, r io.Reader) error {
	base := l.abs(q.BaseRel)
	if q.Clean {
		if err := os.RemoveAll(base); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return err
	}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name := filepath.FromSlash(hdr.Name)
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) || strings.Contains(name, string(filepath.Separator)+".."+string(filepath.Separator)) {
			return fmt.Errorf("refusing tar entry %q", hdr.Name)
		}
		dest := filepath.Join(base, name)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(hdr.Mode).Perm()
		if q.Private {
			mode = 0o600
		}
		f, err := os.OpenFile(dest+".ferry-part", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, tr); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		if err := os.Chmod(dest+".ferry-part", mode); err != nil {
			return err
		}
		if err := os.Rename(dest+".ferry-part", dest); err != nil {
			return err
		}
	}
}

// transfer streams Export from src into Import on dst.
func transfer(src Machine, ex ExportReq, dst Machine, im ImportReq) error {
	pr, pw := io.Pipe()
	errc := make(chan error, 1)
	go func() {
		err := src.Export(ex, pw)
		pw.CloseWithError(err)
		errc <- err
	}()
	importErr := dst.Import(im, pr)
	pr.CloseWithError(importErr)
	exportErr := <-errc
	if exportErr != nil {
		return exportErr
	}
	return importErr
}
