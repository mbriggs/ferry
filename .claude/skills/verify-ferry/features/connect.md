# Connect a host and check it

`ferry setup HOST [USER@HOST]` writes HOST's line into `~/.config/ferry/hosts`, checks ssh, installs this ferry build on the far side, and ends with doctor's checks for that host. `ferry doctor [HOST...]` checks this machine (ferry's name, git, rsync 3.2.5 or newer, agents, rex, `~/src`), then each named host, or every host in the hosts file: the hosts entry, ssh, the far side's ferry version, distinct names, rsync, agents, rex, and the task records. `ferry push [HOST...]` only installs this build on each host, or every host in the hosts file, that runs a different one. Each exits 1 if any check or host fails.

## Sub-features

- `setup-host-known` with a host whose entry is already an `exec:` target prints `hosts kant -> exec:... (already set)`, skips ssh and the install, runs the host checks, and leaves the hosts file unchanged.
- `setup-host-dry` with `--dry-run` prints `hosts would set NAME -> TARGET` and `dry run, nothing written`, exits 0, and skips doctor's checks, which would read the entry it didn't write. An ssh target still gets the ssh check and a `would install` line first.
- `setup-host-new` with a new name and an ssh target appends `NAME TARGET` to the hosts file, then fails at the ssh check and exits 1.
- `doctor-ok` prints `this machine (laptop)` and a `kant` section of `ok` lines, and exits 0.
- `doctor-hub` run on kant checks kant itself, then `laptop` through kant's hosts file.
- `push-exec` skips an `exec:` host: `ferry kant runs from this machine (exec:...); nothing to push`, exit 0.

## How to get to it (user POV)

- `ferry setup kant` (or `ferry setup kant me@kant`, `--dry-run`) on the laptop, once per machine it hands work to.
- `ferry doctor` or `ferry doctor kant` on either machine, when a handoff fails to connect.
- `ferry push` (or `ferry push kant`, `--dry-run`) after upgrading ferry here.

## Driving it with world.sh

Preconditions:

- A fresh world. `source $R/env`, then `cp $LAPTOP/.config/ferry/hosts $R/hosts.orig` to compare against.

- **Known host (`setup-host-known`).** Run `$W ferry $R setup kant`. The output starts with `hosts kant -> exec:<kant home> (already set)`, then a `kant` section with `ok` for hosts, ferry, names, rsync, agents (`claude 0.0.0, codex 0.0.0`), rex, and records. Exit 0. `cmp $LAPTOP/.config/ferry/hosts $R/hosts.orig` succeeds.
- **Dry run (`setup-host-dry`).** Run `$W ferry $R setup kant2 exec:$KANT --dry-run`. Output `hosts would set kant2 -> exec:...` and `dry run, nothing written`, no `kant2` checks, exit 0. The hosts file is unchanged.
- **New host (`setup-host-new`).** Run `$W ferry $R setup kant2 me@kant2`. Output `hosts kant2 -> me@kant2, in ~/.config/ferry/hosts`, then `ssh me@kant2: world.sh: blocked ssh ...`, exit 1. `diff $R/hosts.orig $LAPTOP/.config/ferry/hosts` shows `> kant2 me@kant2`.
- **Doctor (`doctor-ok`).** In a fresh world, run `$W ferry $R doctor`. A `this machine (laptop)` section, a `kant` section, every line `ok`, exit 0.
- **Doctor on the hub (`doctor-hub`).** Run `$W ferry $R --as kant doctor`. A `this machine (kant)` section, then `laptop`, exit 0. Before the first handoff, kant warns `~/src doesn't exist`.
- **Push (`push-exec`).** Run `$W ferry $R push`. Output `ferry kant runs from this machine (exec:...); nothing to push`, exit 0.

## Gotchas

- Run `setup-host-new` last, or in its own world. After it, the hosts file lists `kant2`, so a plain `ferry doctor` checks kant2 too and fails.
- Never point `setup` at a new target for `kant`. That rewrites the world's `exec:` line, and every later step goes to the ssh stub.
- The install branch (`uname` and copying a binary over ssh) can't run in a world, since the ssh stub blocks it, and `ferry push` skips `exec:` hosts. `setup_test.go` covers both with a fake ssh.
- The claude and codex stubs answer `--version` with `0.0.0` (or the home's `.stub-version`) so the agents checks pass. Every other call to them fails with `world.sh: blocked ...`.
