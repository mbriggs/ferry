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
	"regexp"
	"strconv"
	"strings"
	"syscall"
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

// ExtrasList is what ListExtras found: the files to send, and the ignored
// paths left behind, so what stays is visible in the output.
type ExtrasList struct {
	Files      []FileEntry `json:"files"`
	LeftBehind []string    `json:"left_behind,omitempty"`
}

// ListExtras returns the ignored files in a worktree, which travel with it
// next to the git snapshot (tracked and untracked files ride in git). All of
// them go except:
//
//   - dependency and build trees (heavyDirs), which are built for one OS and
//     rebuilt by restore,
//   - git worktrees Claude keeps under .claude/worktrees,
//   - paths matching .ferry/exclude: one pattern per line, a glob against
//     the base name or the whole path, or a directory when it ends in /.
//
// .ferry/state always travels, even when it isn't ignored.
func (l *Local) ListExtras(wtRel string) (ExtrasList, error) {
	root := l.abs(wtRel)
	var exclude []string
	if data, err := os.ReadFile(filepath.Join(root, ".ferry", "exclude")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
				exclude = append(exclude, line)
			}
		}
	}
	stays := func(rel string) bool {
		return neverExtra(rel) || (matchAny(exclude, rel) && !within(filepath.ToSlash(rel), stateDirRel))
	}
	out, err := git(root, "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory")
	if err != nil {
		return ExtrasList{}, err
	}
	hashes := l.hashCache()
	defer hashes.save()
	seen := map[string]bool{}
	var entries []FileEntry
	var left []string
	add := func(rel string) {
		if seen[rel] {
			return
		}
		p := filepath.Join(root, rel)
		if st, err := os.Lstat(p); err == nil && st.Mode().IsRegular() {
			if sum, err := hashes.sha(p); err == nil {
				seen[rel] = true
				entries = append(entries, FileEntry{Path: rel, Size: st.Size(), SHA: sum})
			}
		}
	}
	for _, rel := range strings.Split(out, "\x00") {
		if rel == "" {
			continue
		}
		dir := strings.TrimSuffix(rel, "/")
		switch {
		case inHeavyDir(dir) || stays(dir):
			if !neverExtra(dir) {
				left = append(left, rel)
			}
		case strings.HasSuffix(rel, "/"):
			// git collapsed an ignored directory; take the files inside,
			// skipping dependency and build trees and excluded paths.
			walkIgnored(filepath.Join(root, dir), func(p string) {
				r, _ := filepath.Rel(root, p)
				if !stays(r) {
					add(r)
				}
			})
		default:
			add(rel)
		}
	}
	// .ferry/state may be untracked but not ignored; it's excluded from the
	// snapshot either way, so it always travels as an extra.
	walkFiles(filepath.Join(root, stateDirRel), func(p string) {
		r, _ := filepath.Rel(root, p)
		add(r)
	})
	return ExtrasList{Files: entries, LeftBehind: left}, nil
}

// inHeavyDir reports whether any directory in rel is a dependency or build
// tree.
func inHeavyDir(rel string) bool {
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if heavyDirs[part] {
			return true
		}
	}
	return false
}

func neverExtra(rel string) bool {
	return within(filepath.ToSlash(rel), ".claude/worktrees") || filepath.Base(rel) == ".DS_Store"
}

// matchAny reports whether rel matches a pattern: a glob against the base
// name or the whole path, or, for a pattern ending in /, anything under that
// directory.
func matchAny(patterns []string, rel string) bool {
	rel = filepath.ToSlash(rel)
	for _, p := range patterns {
		if dir, ok := strings.CutSuffix(p, "/"); ok {
			if within(rel, dir) {
				return true
			}
			continue
		}
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
	"__pycache__": true, ".pytest_cache": true, ".mypy_cache": true, ".ruff_cache": true, ".tox": true,
	".gradle": true, ".terraform": true, ".turbo": true, ".parcel-cache": true, ".svelte-kit": true, ".nuxt": true,
	".elixir_ls": true, ".expo": true, "elm-stuff": true, "coverage": true,
}

func walkIgnored(dir string, fn func(string)) {
	if heavyDirs[filepath.Base(dir)] {
		return
	}
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && p != dir && (heavyDirs[d.Name()] || (d.Name() == "worktrees" && filepath.Base(filepath.Dir(p)) == ".claude")) {
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
	// SHAs are the files' sha256 as listed before the transfer, by path
	// relative to BaseRel. The receiver names partial files after them, so a
	// later transfer of the same content can resume.
	SHAs map[string]string `json:"shas,omitempty"`
	// Offsets skip the first bytes of files the receiver already holds part of.
	Offsets map[string]int64 `json:"offsets,omitempty"`
}

// sumsName is the last entry of every export: "sha256 path" per file, so the
// receiver can check what it wrote against what was read here.
const sumsName = ".ferry-sums"

const (
	paxSHA    = "FERRY.sha"
	paxOffset = "FERRY.offset"
)

// Export writes a tar of the given paths (files or whole directories),
// relative to BaseRel, to w, ending with a sumsName entry. A resumed file's
// entry holds only the bytes after its offset; its sum covers the whole file.
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
			name := filepath.ToSlash(rel)
			if name == sumsName {
				return fmt.Errorf("can't send a file named %s", sumsName)
			}
			off := q.Offsets[name]
			if off < 0 || off > info.Size() {
				off = 0
			}
			hdr := &tar.Header{Name: name, Mode: int64(info.Mode().Perm()), Size: info.Size() - off, ModTime: info.ModTime(), Typeflag: tar.TypeReg, Format: tar.FormatPAX, PAXRecords: map[string]string{}}
			if sha := q.SHAs[name]; sha != "" {
				hdr.PAXRecords[paxSHA] = sha
			}
			if off > 0 {
				hdr.PAXRecords[paxOffset] = strconv.FormatInt(off, 10)
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			h := sha256.New()
			if _, err := io.CopyN(h, f, off); err != nil {
				return err
			}
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if _, err := io.Copy(io.MultiWriter(tw, h), f); err != nil {
				return err
			}
			fmt.Fprintf(&sums, "%x %s\n", h.Sum(nil), name)
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

var shaPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// partPath is where Import writes a file until the whole transfer checks out.
// A file sent with its sha gets a name tied to that content, so a part left
// by a failed transfer is only ever resumed with the same content.
func partPath(dest, sha string) string {
	if shaPattern.MatchString(sha) {
		return dest + ".ferry-part-" + sha[:16]
	}
	return dest + ".ferry-part"
}

// Import extracts a tar from r under BaseRel. Entries that would land outside
// BaseRel are rejected. Every file is written to a part file, hashed, and
// synced, and the stream must end with sums matching what was written. Only
// then do the parts take their real names, so a stream cut short or altered
// fails here with nothing installed, and leaves parts a rerun can resume.
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
	parts := map[string][2]string{} // name -> part, dest
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
			bad, err := checkSums(tr, written)
			if err != nil {
				if p, ok := parts[bad]; ok {
					os.Remove(p[0])
				}
				return err
			}
			if _, err := tr.Next(); !errors.Is(err, io.EOF) {
				return errors.New("the transfer continued past its checksums")
			}
			for _, p := range parts {
				if err := os.Rename(p[0], p[1]); err != nil {
					return err
				}
			}
			_, err = io.Copy(io.Discard, r)
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
		part := partPath(dest, hdr.PAXRecords[paxSHA])
		off, _ := strconv.ParseInt(hdr.PAXRecords[paxOffset], 10, 64)
		h := sha256.New()
		var f *os.File
		if off > 0 {
			if f, err = os.OpenFile(part, os.O_RDWR, mode); err != nil {
				return fmt.Errorf("can't resume %s: %w", hdr.Name, err)
			}
			if err := f.Truncate(off); err != nil {
				f.Close()
				return err
			}
			if n, err := io.CopyN(h, f, off); err != nil || n != off {
				f.Close()
				return fmt.Errorf("can't resume %s: the partial copy is short", hdr.Name)
			}
		} else if f, err = os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode); err != nil {
			return err
		}
		if _, err := io.Copy(io.MultiWriter(f, h), tr); err != nil {
			f.Sync()
			f.Close()
			return err
		}
		written[hdr.Name] = hex.EncodeToString(h.Sum(nil))
		if want := hdr.PAXRecords[paxSHA]; want != "" && want != written[hdr.Name] {
			// The file changed between being listed and being read.
			f.Close()
			os.Remove(part)
			return fmt.Errorf("%s changed while it was being sent; run this again", hdr.Name)
		}
		if err := f.Sync(); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		if err := os.Chmod(part, mode); err != nil {
			return err
		}
		parts[hdr.Name] = [2]string{part, dest}
	}
}

// checkSums compares the sender's sums with what was written, and names the
// file that differs, if one does.
func checkSums(r io.Reader, written map[string]string) (string, error) {
	data, err := io.ReadAll(io.LimitReader(r, 64<<20))
	if err != nil {
		return "", err
	}
	listed := 0
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		sum, name, ok := strings.Cut(line, " ")
		if !ok {
			return "", fmt.Errorf("bad checksum line %q", line)
		}
		got, have := written[name]
		switch {
		case !have:
			return name, fmt.Errorf("%s was sent but never arrived", name)
		case got != sum:
			return name, fmt.Errorf("%s arrived different from what was sent (sha256 %s, want %s)", name, got[:12], sum[:min(12, len(sum))])
		}
		listed++
	}
	if listed != len(written) {
		return "", errors.New("the transfer carried files its checksums don't list")
	}
	return "", nil
}

type PlanReq struct {
	WtRel    string      `json:"wt_rel"`
	StageRel string      `json:"stage_rel"`
	Files    []FileEntry `json:"files"`
}

type ExtrasPlan struct {
	// Have lists files this side already has with the same content.
	Have []string `json:"have,omitempty"`
	// Partial is how much of each file an earlier, interrupted transfer
	// left in staging.
	Partial map[string]int64 `json:"partial,omitempty"`
}

// PlanExtras says which extras needn't be sent, and where interrupted
// transfers can resume. A file counts as had only when size and sha256 match,
// and the size check comes first, so large changed files aren't hashed.
func (l *Local) PlanExtras(q PlanReq) (ExtrasPlan, error) {
	root, stage := l.abs(q.WtRel), l.abs(q.StageRel)
	plan := ExtrasPlan{Partial: map[string]int64{}}
	hashes := l.hashCache()
	defer hashes.save()
	for _, e := range q.Files {
		if safeDest(root, e.Path) != nil || safeDest(stage, e.Path) != nil {
			continue
		}
		p := filepath.Join(root, e.Path)
		if st, err := os.Lstat(p); err == nil && st.Mode().IsRegular() && st.Size() == e.Size {
			if sum, err := hashes.sha(p); err == nil && sum == e.SHA {
				plan.Have = append(plan.Have, e.Path)
				continue
			}
		}
		if st, err := os.Lstat(partPath(filepath.Join(stage, e.Path), e.SHA)); err == nil && st.Mode().IsRegular() && st.Size() <= e.Size {
			plan.Partial[filepath.ToSlash(e.Path)] = st.Size()
		}
	}
	return plan, nil
}

type InstallExtrasReq struct {
	WtRel    string `json:"wt_rel"`
	StageRel string `json:"stage_rel"`
	Task     string `json:"task"`
	// Files lists the extras just staged. Only these are installed, so
	// anything else in staging from an earlier attempt stays out.
	Files []FileEntry `json:"files,omitempty"`
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
// write anywhere in this home), and nothing goes under .git. Files are
// renamed from staging, which is on the same filesystem as home, so a large
// dump isn't copied again; across filesystems they're streamed into a fresh
// temp file and renamed.
//
// Removals never delete: a removed extra moves to
// ~/.local/state/ferry/parked/<task>-extras-<time>/.
func (l *Local) InstallExtras(q InstallExtrasReq) (InstallExtrasResult, error) {
	var res InstallExtrasResult
	root, stage := l.abs(q.WtRel), l.abs(q.StageRel)
	defer os.RemoveAll(stage)
	hashes := l.hashCache()
	defer hashes.save()
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
			if sum, err := hashes.sha(p); err != nil || sum != e.SHA {
				res.Kept = append(res.Kept, e.Path)
				continue
			}
			if err := moveFile(p, filepath.Join(dir, e.Path)); err != nil {
				return res, err
			}
			res.Removed = append(res.Removed, e.Path)
		}
	}
	for _, e := range q.Files {
		if err := safeDest(root, e.Path); err != nil {
			return res, err
		}
		if err := safeDest(stage, e.Path); err != nil {
			return res, err
		}
		from, dest := filepath.Join(stage, e.Path), filepath.Join(root, e.Path)
		if st, err := os.Lstat(from); err != nil || !st.Mode().IsRegular() {
			return res, fmt.Errorf("extra %s wasn't staged", e.Path)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return res, err
		}
		if err := os.Chmod(from, 0o600); err != nil {
			return res, err
		}
		err := os.Rename(from, dest)
		if errors.Is(err, syscall.EXDEV) {
			err = copyInto(from, dest)
		}
		if err != nil {
			return res, err
		}
		// Import checked these bytes against the sender's sum.
		if st, err := os.Lstat(dest); err == nil && e.SHA != "" {
			hashes.put(dest, st, e.SHA)
		}
	}
	return res, nil
}

// copyInto streams from into a new temp file beside dest and renames it over
// dest.
func copyInto(from, dest string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".ferry-extra-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dest)
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
