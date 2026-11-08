package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

// cmdPush puts this build of ferry on each named host, or every host in
// ~/.config/ferry/hosts, that runs a different one. A host that fails
// doesn't stop the rest.
func cmdPush(l *Local, args []string) int {
	fs := flag.NewFlagSet("push", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "show what would be installed without installing it")
	hosts, err := parse(fs, args)
	if err != nil {
		return 2
	}
	named := len(hosts) > 0
	if !named {
		hosts = l.hostNames()
		if len(hosts) == 0 {
			return fail(fmt.Errorf("no hosts in ~/.config/ferry/hosts; add one with `ferry setup HOST [USER@HOST]`"))
		}
	}
	failed := false
	for _, name := range hosts {
		if l.isSelf(name) {
			if named {
				fmt.Fprintf(os.Stderr, "ferry: %s is this machine\n", name)
				failed = true
			}
			continue
		}
		target, ferryPath, _ := l.hostEntry(name)
		if strings.HasPrefix(target, "exec:") {
			step("ferry", "%s runs from this machine (%s); nothing to push", name, target)
			continue
		}
		if _, err := sshCheck(target); err != nil {
			fmt.Fprintf(os.Stderr, "ferry: %s: ssh %s: %v\n", name, target, err)
			if fix := sshFix(err.Error()); fix != "" {
				fmt.Fprintln(os.Stderr, "  fix:", fix)
			}
			failed = true
			continue
		}
		if err := pushFerry(name, target, ferryPath, *dryRun); err != nil {
			fmt.Fprintf(os.Stderr, "ferry: %s: %v\n", name, err)
			failed = true
		}
	}
	if failed {
		return 1
	}
	return 0
}
