package main

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type FileEntry struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	SHA  string `json:"sha,omitempty"`
}

func fileSHA(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
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
		p := filepath.Join(root, rel)
		if st, err := os.Lstat(p); err == nil && st.Mode().IsRegular() {
			if sum, err := fileSHA(p); err == nil {
				seen[rel] = true
				entries = append(entries, FileEntry{Path: rel, Size: st.Size(), SHA: sum})
			}
		}
	}
	for _, rel := range strings.Split(out, "\x00") {
		if rel == "" {
			continue
		}
		if strings.HasSuffix(rel, "/") {
			// git collapsed an ignored directory; look inside it for files
			// the include patterns ask for, skipping dependency and build trees.
			walkIgnored(filepath.Join(root, strings.TrimSuffix(rel, "/")), func(p string) {
				r, _ := filepath.Rel(root, p)
				if strings.HasPrefix(r, stateDirRel+string(filepath.Separator)) || matchAny(patterns, r) {
					add(r)
				}
			})
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

// heavyDirs are never searched for extras: they're rebuilt, not moved.
var heavyDirs = map[string]bool{
	"node_modules": true, "_build": true, "deps": true, "target": true, ".venv": true, "venv": true,
	"vendor": true, "dist": true, "build": true, ".next": true, ".cache": true, "tmp": true, "log": true, ".git": true,
}

func walkIgnored(dir string, fn func(string)) {
	if heavyDirs[filepath.Base(dir)] {
		return
	}
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && p != dir && heavyDirs[d.Name()] {
			return filepath.SkipDir
		}
		if d.Type().IsRegular() {
			fn(p)
		}
		return nil
	})
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

// sumsName is the last entry of every export: "sha256 path" per file, so the
// receiver can check what it wrote against what was read here.
const sumsName = ".ferry-sums"

// Export writes a tar of the given paths (files or whole directories),
// relative to BaseRel, to w, ending with a sumsName entry.
func (l *Local) Export(q ExportReq, w io.Writer) error {
	base := l.abs(q.BaseRel)
	tw := tar.NewWriter(w)
	var sums strings.Builder
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
			if filepath.ToSlash(rel) == sumsName {
				return fmt.Errorf("can't send a file named %s", sumsName)
			}
			hdr := &tar.Header{Name: filepath.ToSlash(rel), Mode: int64(info.Mode().Perm()), Size: info.Size(), ModTime: info.ModTime(), Typeflag: tar.TypeReg}
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			h := sha256.New()
			if _, err := io.Copy(io.MultiWriter(tw, h), f); err != nil {
				return err
			}
			fmt.Fprintf(&sums, "%x %s\n", h.Sum(nil), hdr.Name)
			return nil
		})
		if err != nil {
			return err
		}
	}
	if err := tw.WriteHeader(&tar.Header{Name: sumsName, Mode: 0o600, Size: int64(sums.Len()), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	if _, err := io.WriteString(tw, sums.String()); err != nil {
		return err
	}
	return tw.Close()
}

type ImportReq struct {
	BaseRel string `json:"base_rel"`
	Clean   bool   `json:"clean"`   // empty BaseRel first (staging directories)
	Private bool   `json:"private"` // write files 0600 (secrets)
}

// Import extracts a tar from r under BaseRel. Entries that would land outside
// BaseRel are rejected. Every file is hashed as it's written and synced to
// disk, and the stream must end with sums matching what was written; a
// stream cut short or altered fails here, before anything is installed.
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
	written := map[string]string{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return errors.New("the transfer ended before its checksums arrived; nothing was installed")
		} else if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if hdr.Name == sumsName {
			if err := checkSums(tr, written); err != nil {
				return err
			}
			if _, err := tr.Next(); !errors.Is(err, io.EOF) {
				return errors.New("the transfer continued past its checksums")
			}
			_, err := io.Copy(io.Discard, r)
			return err
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
		h := sha256.New()
		if _, err := io.Copy(io.MultiWriter(f, h), tr); err != nil {
			f.Close()
			return err
		}
		written[hdr.Name] = hex.EncodeToString(h.Sum(nil))
		if err := f.Sync(); err != nil {
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

func checkSums(r io.Reader, written map[string]string) error {
	data, err := io.ReadAll(io.LimitReader(r, 64<<20))
	if err != nil {
		return err
	}
	listed := 0
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		sum, name, ok := strings.Cut(line, " ")
		if !ok {
			return fmt.Errorf("bad checksum line %q", line)
		}
		got, have := written[name]
		switch {
		case !have:
			return fmt.Errorf("%s was sent but never arrived", name)
		case got != sum:
			return fmt.Errorf("%s arrived different from what was sent (sha256 %s, want %s)", name, got[:12], sum[:min(12, len(sum))])
		}
		listed++
	}
	if listed != len(written) {
		return errors.New("the transfer carried files its checksums don't list")
	}
	return nil
}

type InstallExtrasReq struct {
	WtRel    string `json:"wt_rel"`
	StageRel string `json:"stage_rel"`
	Task     string `json:"task"`
	// Remove lists extras this machine sent last time that the other side no
	// longer has. Each is parked if it's still exactly what was sent.
	Remove []FileEntry `json:"remove,omitempty"`
}

type InstallExtrasResult struct {
	Removed []string `json:"removed,omitempty"`
	Kept    []string `json:"kept,omitempty"` // changed here since they were sent
}

// InstallExtras moves staged extras into the worktree. The worktree's tracked
// contents came from the other machine, so every path is checked here: no
// component may be a symlink (a tracked symlink could otherwise redirect a
// write anywhere in this home), nothing goes under .git, and files are
// written through a fresh, randomly named temp file and renamed into place.
//
// Removals never delete: a removed extra moves to
// ~/.local/state/ferry/parked/<task>-extras-<time>/.
func (l *Local) InstallExtras(q InstallExtrasReq) (InstallExtrasResult, error) {
	var res InstallExtrasResult
	root, stage := l.abs(q.WtRel), l.abs(q.StageRel)
	defer os.RemoveAll(stage)
	if len(q.Remove) > 0 {
		dir, err := l.parkDir(q.Task, "extras-"+time.Now().Format("20060102-150405"))
		if err != nil {
			return res, err
		}
		for _, e := range q.Remove {
			if err := safeDest(root, e.Path); err != nil {
				return res, err
			}
			p := filepath.Join(root, e.Path)
			if !exists(p) {
				continue
			}
			if sum, err := fileSHA(p); err != nil || sum != e.SHA {
				res.Kept = append(res.Kept, e.Path)
				continue
			}
			if err := moveFile(p, filepath.Join(dir, e.Path)); err != nil {
				return res, err
			}
			res.Removed = append(res.Removed, e.Path)
		}
	}
	if !isDir(stage) {
		return res, nil
	}
	return res, filepath.WalkDir(stage, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(stage, p)
		if !d.Type().IsRegular() {
			return fmt.Errorf("refusing extra %s: not a regular file", rel)
		}
		if err := safeDest(root, rel); err != nil {
			return err
		}
		dest := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		tmp, err := os.CreateTemp(filepath.Dir(dest), ".ferry-extra-*")
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())
		if _, err := tmp.Write(data); err != nil {
			tmp.Close()
			return err
		}
		if err := tmp.Chmod(0o600); err != nil {
			tmp.Close()
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		return os.Rename(tmp.Name(), dest)
	})
}

// safeDest checks that writing root/rel stays inside root: rel is relative and
// clean, no component is ".git", and no existing component along the way
// (including the file itself) is a symlink or a non-directory.
func safeDest(root, rel string) error {
	clean := filepath.Clean(rel)
	if filepath.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("refusing extra %q: outside the worktree", rel)
	}
	parts := strings.Split(clean, string(filepath.Separator))
	cur := root
	for i, part := range parts {
		if part == ".git" {
			return fmt.Errorf("refusing extra %q: inside .git", rel)
		}
		cur = filepath.Join(cur, part)
		st, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		last := i == len(parts)-1
		switch {
		case st.Mode()&fs.ModeSymlink != 0:
			return fmt.Errorf("refusing extra %q: %s is a symlink", rel, cur)
		case !last && !st.IsDir():
			return fmt.Errorf("refusing extra %q: %s is not a directory", rel, cur)
		case last && !st.Mode().IsRegular():
			return fmt.Errorf("refusing extra %q: %s is not a regular file", rel, cur)
		}
	}
	return nil
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
