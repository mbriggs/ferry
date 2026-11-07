package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Git hooks live in the repo's .git/hooks, which git never transfers, so a
// repo created on the other machine by ferry has none. They travel by name,
// content and mode. Samples git writes itself (*.sample) stay.

type GitHook struct {
	Name    string      `json:"name"`
	Mode    fs.FileMode `json:"mode"`
	Content []byte      `json:"content"`
}

type InstallGitHooksReq struct {
	RepoRel string    `json:"repo_rel"`
	Hooks   []GitHook `json:"hooks,omitempty"` // sent on stdin by Remote
}

var hookName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

const maxGitHookSize = 1 << 20

// hooksDir is the hooks directory of the repo at repoRel, which must be
// inside home.
func (l *Local) hooksDir(repoRel string) (string, error) {
	common, err := git(l.abs(repoRel), "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	home, _ := filepath.EvalSymlinks(l.Home)
	common, _ = filepath.EvalSymlinks(common)
	if !strings.HasPrefix(common, home+string(filepath.Separator)) {
		return "", fmt.Errorf("git dir %s is outside home", common)
	}
	return filepath.Join(common, "hooks"), nil
}

// GitHooks returns the repo's hooks, sorted by name.
func (l *Local) GitHooks(repoRel string) ([]GitHook, error) {
	dir, err := l.hooksDir(repoRel)
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var out []GitHook
	for _, e := range ents {
		if !e.Type().IsRegular() || strings.HasSuffix(e.Name(), ".sample") || !hookName.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil || info.Size() > maxGitHookSize {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, GitHook{Name: e.Name(), Mode: info.Mode().Perm(), Content: data})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// InstallGitHooks writes hooks into the repo, parking any it replaces.
func (l *Local) InstallGitHooks(q InstallGitHooksReq) error {
	dir, err := l.hooksDir(q.RepoRel)
	if err != nil {
		return err
	}
	parked := l.stateDir("parked", "git-hooks-"+taskKey(q.RepoRel)+"-"+time.Now().Format("20060102-150405"))
	for _, h := range q.Hooks {
		if !hookName.MatchString(h.Name) || strings.HasSuffix(h.Name, ".sample") || len(h.Content) > maxGitHookSize {
			return fmt.Errorf("refusing git hook %q", h.Name)
		}
		dest := filepath.Join(dir, h.Name)
		if st, err := os.Lstat(dest); err == nil {
			if !st.Mode().IsRegular() {
				return fmt.Errorf("%s isn't a regular file; not replacing it", dest)
			}
			if err := moveFile(dest, filepath.Join(parked, h.Name)); err != nil {
				return err
			}
		}
		if err := writeAtomic(dest, h.Content, h.Mode&0o755|0o600); err != nil {
			return err
		}
	}
	return nil
}

// hooksToSend returns the source's hooks that the receiver lacks or has
// with different content or mode.
func hooksToSend(src, dst []GitHook) []GitHook {
	have := map[string]GitHook{}
	for _, h := range dst {
		have[h.Name] = h
	}
	var out []GitHook
	for _, h := range src {
		if d, ok := have[h.Name]; !ok || d.Mode != h.Mode || !bytes.Equal(d.Content, h.Content) {
			out = append(out, h)
		}
	}
	return out
}

func (r *Remote) GitHooks(repoRel string) ([]GitHook, error) {
	return rpc[[]GitHook](r, "git-hooks", repoRel)
}

// InstallGitHooks sends the hooks on stdin: they can be larger than a
// command line holds.
func (r *Remote) InstallGitHooks(q InstallGitHooksReq) error {
	body, err := json.Marshal(q.Hooks)
	if err != nil {
		return err
	}
	var v struct{}
	return r.call("install-git-hooks", InstallGitHooksReq{RepoRel: q.RepoRel}, &v, bytes.NewReader(body), nil)
}

func serveInstallGitHooks(l *Local, raw []byte, stdin io.Reader) error {
	q, err := decode[InstallGitHooksReq](raw)
	if err != nil {
		return err
	}
	if err := json.NewDecoder(io.LimitReader(stdin, 64<<20)).Decode(&q.Hooks); err != nil {
		return fmt.Errorf("git hooks: %v", err)
	}
	return l.InstallGitHooks(q)
}
