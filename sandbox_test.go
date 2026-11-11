package main

import "testing"

func TestBoxName(t *testing.T) {
	for in, want := range map[string]string{
		"fix-login":          "fix-login-a1b2c3",
		"Fix_Login Redirect": "fix-login-redirect-a1b2c3",
		"2026-cleanup":       "t-2026-cleanup-a1b2c3",
		"...":                "task-a1b2c3",
		"a-very-long-task-name-that-goes-past-the-limit": "a-very-long-task-name-tha-a1b2c3",
	} {
		if got := boxName(task{Name: in, Key: "src-repo-" + in + "-a1b2c3"}); got != want {
			t.Errorf("boxName(%q) = %q, want %q", in, got, want)
		}
	}
	// Two worktrees with the same name are two tasks, so two boxes.
	a, b := boxName(task{Name: "fix", Key: taskKey("src/a/fix")}), boxName(task{Name: "fix", Key: taskKey("src/b/fix")})
	if a == b {
		t.Errorf("tasks src/a/fix and src/b/fix share box %s", a)
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
