package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBoxName(t *testing.T) {
	for in, want := range map[string]string{
		"fix-login":          "fix-login",
		"Fix_Login Redirect": "fix-login-redirect",
		"2026-cleanup":       "t-2026-cleanup",
		"...":                "task",
		"a-very-long-task-name-that-goes-past-the-limit": "a-very-long-task-name-that-goes",
	} {
		if got := boxName(task{Name: in}); got != want {
			t.Errorf("boxName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSandboxTarget(t *testing.T) {
	for in, want := range map[string][2]string{
		"fix-login.kant.box": {"fix-login", "kant"},
		"kant":               {},
		"kant.box":           {},
		".kant.box":          {},
		"fix.kant.local":     {},
	} {
		name, host, ok := sandboxTarget(in)
		if got := [2]string{name, host}; ok != (want != [2]string{}) || (ok && got != want) {
			t.Errorf("sandboxTarget(%q) = %q, %q, %v; want %q", in, name, host, ok, want)
		}
	}
}

func TestForgetHostKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	os.WriteFile(path, []byte("a.kant.box ssh-ed25519 AAAA1\nb.kant.box ssh-ed25519 AAAA2\na.kant.box ssh-ed25519 AAAA3\n"), 0o600)
	if err := forgetHostKey(path, "a.kant.box"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "b.kant.box ssh-ed25519 AAAA2\n" {
		t.Fatalf("known_hosts = %q", got)
	}
}
