package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// claudeRules are the Claude Code permission rules ferry's development needs.
// They're scoped to throwaway test slugs, so no agent can rewrite real session
// history; auto mode otherwise blocks writes under ~/.claude/projects.
var claudeRules = []string{
	"Edit(~/.claude/projects/-tmp-ferry-test-*/**)",
}

// cmdSetup adds claudeRules to ~/.claude/settings.json. It writes through a
// symlink (settings are often stowed from a dotfiles repo) and keeps a backup
// in ~/.local/state/ferry, outside any repo.
func cmdSetup(l *Local, args []string) int {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "show the change without writing it")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	path := filepath.Join(l.Home, ".claude", "settings.json")
	target := path
	if real, err := filepath.EvalSymlinks(path); err == nil {
		target = real
	}
	data, err := os.ReadFile(target)
	if os.IsNotExist(err) {
		data = []byte("{}")
	} else if err != nil {
		return fail(err)
	}
	root, err := decodeOrdered(data)
	if err != nil {
		return fail(fmt.Errorf("%s: %w", target, err))
	}
	settings, ok := root.(*ordered)
	if !ok {
		return fail(fmt.Errorf("%s: not a JSON object", target))
	}
	perms, _ := settings.get("permissions").(*ordered)
	if perms == nil {
		perms = &ordered{}
		settings.set("permissions", perms)
	}
	allow, _ := perms.get("allow").([]any)
	have := map[string]bool{}
	for _, r := range allow {
		if s, ok := r.(string); ok {
			have[s] = true
		}
	}
	var added []string
	for _, r := range claudeRules {
		if !have[r] {
			allow = append(allow, r)
			added = append(added, r)
		}
	}
	if len(added) == 0 {
		fmt.Println("claude permissions: already set in", target)
		return 0
	}
	for _, r := range added {
		fmt.Println("claude permissions: adding", r, "to", target)
	}
	if *dryRun {
		fmt.Println("dry run, nothing written")
		return 0
	}
	perms.set("allow", allow)
	out, err := encodeOrdered(settings)
	if err != nil {
		return fail(err)
	}
	backup := l.stateDir("backups", "claude-settings-"+time.Now().Format("20060102-150405")+".json")
	if err := os.MkdirAll(filepath.Dir(backup), 0o700); err != nil {
		return fail(err)
	}
	if err := os.WriteFile(backup, data, 0o600); err != nil {
		return fail(err)
	}
	mode := os.FileMode(0o644)
	if st, err := os.Stat(target); err == nil {
		mode = st.Mode().Perm()
	}
	if err := writeAtomic(target, append(out, '\n'), mode); err != nil {
		return fail(err)
	}
	fmt.Println("backup at", backup)
	return 0
}

// ordered is a JSON object that keeps its key order, so editing a settings
// file someone keeps in git changes only the lines it means to.
type ordered struct {
	keys []string
	vals map[string]any
}

func (o *ordered) get(k string) any { return o.vals[k] }

func (o *ordered) set(k string, v any) {
	if o.vals == nil {
		o.vals = map[string]any{}
	}
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

func decodeOrdered(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing data after JSON value")
	}
	return v, nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch tok {
	case json.Delim('{'):
		o := &ordered{}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return nil, err
			}
			v, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			o.set(kt.(string), v)
		}
		_, err := dec.Token()
		return o, err
	case json.Delim('['):
		arr := []any{}
		for dec.More() {
			v, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
		}
		_, err := dec.Token()
		return arr, err
	}
	return tok, nil
}

func encodeOrdered(v any) ([]byte, error) {
	var b bytes.Buffer
	if err := encodeValue(&b, v, ""); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func encodeValue(b *bytes.Buffer, v any, ind string) error {
	switch x := v.(type) {
	case *ordered:
		if len(x.keys) == 0 {
			b.WriteString("{}")
			return nil
		}
		b.WriteString("{\n")
		for i, k := range x.keys {
			kb, _ := marshalString(k)
			b.WriteString(ind + "  ")
			b.Write(kb)
			b.WriteString(": ")
			if err := encodeValue(b, x.vals[k], ind+"  "); err != nil {
				return err
			}
			if i < len(x.keys)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(ind + "}")
	case []any:
		if len(x) == 0 {
			b.WriteString("[]")
			return nil
		}
		b.WriteString("[\n")
		for i, e := range x {
			b.WriteString(ind + "  ")
			if err := encodeValue(b, e, ind+"  "); err != nil {
				return err
			}
			if i < len(x)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(ind + "]")
	case string:
		sb, err := marshalString(x)
		if err != nil {
			return err
		}
		b.Write(sb)
	default:
		enc, err := json.Marshal(x)
		if err != nil {
			return err
		}
		b.Write(enc)
	}
	return nil
}
