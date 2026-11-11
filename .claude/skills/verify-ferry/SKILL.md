---
name: verify-ferry
description: Drive the real ferry CLI between two throwaway fake homes and capture evidence. Use before calling a ferry change done, or to reproduce a handoff that behaves oddly.
---

# Verify ferry

ferry is a CLI. A user runs `ferry to kant` inside a repo under `~/src`, and the code, ignored files like `.env`, and the Claude or Codex transcript move to kant; `ferry from kant NAME` brings them back. The user-facing surface is the command's step log (`task`, `agent`, `extras`, `commit`, `resume` lines), its exit code, its prompts on `/dev/tty` (Yes/No forms and a select list in a terminal, plain `[y/N]` without one), and the files it leaves on each machine.

`world.sh` (next to this file) builds a "world": two fake homes, `laptop` and `kant`, under `/tmp/ferry-verify-XXXXXX`. The laptop's hosts file says `kant exec:<kant home>`, ferry's own test transport: every remote call runs `ferry remote OP` with `HOME=<kant home>`, the same JSON protocol it speaks over ssh. Worlds are isolated from each other and from the real `~`, so any number can run side by side.

The feature map in `features/README.md` lists what to drive. Read it, then every feature file with a sub-feature your change touches. A change can span several; extras, for example, appear in hand-off, bring-back, and hooks.

## Hard rules

- Run ferry only through `world.sh ferry` or `world.sh tty`. They set `HOME` to the fake home and put stub `ssh`, `rex`, `claude`, and `codex` first on PATH. The claude and codex stubs answer `--version` with `0.0.0`, or the contents of `$HOME/.stub-version` when that file exists; every other stub call fails with `world.sh: blocked ...`. A bare `ferry` call uses the real `~`, the real kant, real Rex, and a real agent.
- Pass `--no-launch` to `ferry to kant`, and to `ferry from` in the tmux pane. Without it, `to` tries Rex, hits the stub, and prints `launch failed: ... world.sh: blocked rex ...` before the `resume` line. A `from` with a terminal replaces itself with `claude --resume` in that terminal, hits the stub, and exits 1 with `world.sh: blocked claude ...`. Only bring-back.md's `from-exec` and `from-config` steps want that.
- Use only the host names `kant`, and `laptop` when running `--as kant`. ferry sends any other name to ssh, and the stub fails it with `world.sh: blocked ssh ...`. connect.md uses `kant2` on purpose to reach that failure.

## Launch

```
W=$(git rev-parse --show-toplevel)/.claude/skills/verify-ferry/world.sh
R=$($W new)                                # or: $W new --hooks
```

`new` builds the binary from the checkout that holds this skill, including uncommitted edits, into `$R/bin/ferry`, records a hash of the Go source it built from, then creates `laptop/src/demo` with:

- an `origin` bare repo and one unpushed commit (`unpushed.txt`)
- `app.txt` both staged and further modified, `gone.txt` staged for deletion, `untracked.txt`
- ignored `.env` (should travel, mode 0600 on arrival) and `node_modules/x/index.js` (should not travel)
- an idle Claude transcript, session `11111111-2222-3333-4444-555555555555`, mentioning the codeword HERON
- with `--hooks`: committed `.ferry/dehydrate` and `.ferry/restore`, not approved on either side

It's ready when it prints the root and exits 0. `$R/env` holds `ROOT LAPTOP KANT FERRY WT KWT EVIDENCE SID`, plus `CWT KCWT CWSID` for claude-worktree.md, for `source`. Shell variables don't survive between tool calls, so set `W` and `R` and `source $R/env` again in each call.

## Doctor

```
$W doctor $R
```

Prints `ok` or `FAIL` per check, and changes nothing a later check reads. Run it first, and again whenever a result looks wrong.

When you edit ferry's source mid-journey, run `$W rebuild $R`. `world.sh ferry` and `tty` refuse to run a binary older than the source, so you can't verify a stale build by mistake. A rebuild keeps the world's state, so steps after it run the new code against state the old code left. Start a new world when the journey has to run on one build from the top.

If someone else is editing ferry while you drive, every edit makes the world stale. To pin a build, run `git worktree add --detach /tmp/ferry-head HEAD`, and use `/tmp/ferry-head/.claude/skills/verify-ferry/world.sh`, the skill as committed at HEAD. Remove the worktree when you're done.

## Drive

Non-interactive commands, output logged to evidence with the exit code:

```
$W ferry $R to kant --yes --no-launch          # as laptop, in laptop/src/demo
$W ferry $R ls kant
$W ferry $R -C /tmp from kant demo --yes       # from anywhere; -C sets the cwd
$W ferry $R --as kant ls laptop                # run as kant, in kant/src/demo (kant's home before the first handoff)
```

`world.sh ferry` runs ferry in a new session (perl's `setsid`, so it works on macOS and Linux) with stdin closed. ferry reads prompts from `/dev/tty`, and a process with no controlling terminal can't open it, so every prompt answers no, even when you run world.sh from your own terminal. Its exit status is ferry's. `--yes` covers the ignored-files prompt only and never approves hooks. To answer prompts, use the tmux pane (a private server, `tmux -L <world name>`):

```
$W tty $R to kant --yes --no-launch    # starts ferry in the pane
$W wait $R 'run this dehydrate'        # polls the pane for that text, fails after 30s
$W screen $R before-dehydrate          # prints the pane and saves it to evidence
$W press $R Left Enter                 # Yes on a prompt that approves running code (hooks); y there means No
$W keys $R y                           # types y + Enter; Yes on the other Yes/No prompts
$W keys $R                             # Enter alone; picks the highlighted row in a select list
$W wait $R '[exit '                    # the pane prints [exit N] when ferry finishes
$W screen $R after
```

Before each `screen`, `wait` for the text the next step prints. The old prompt stays on screen, so wait for something new.

Three more helpers drive the world itself:

- `$W kant-work $R` plays the resumed agent on kant: commits `kant-was-here.txt` (that file only), appends to `app.txt` unstaged, appends a transcript turn. Use it between `to` and `from`.
- `$W claude-worktree $R closed|kept` stages a worktree Claude made, `$CWT` (`src/demo/.claude/worktrees/feat`), with session `$CWSID`: `closed` as after closing its pane, `kept` as after `/exit` and Keep. claude-worktree.md drives it.
- `$W state $R LABEL` snapshots both homes: git status, log, staged diff, ignored files, `.env` mode, transcripts, parked files, task records (kant holds the records).

To stage your own scenario, edit files under `$LAPTOP` or `$KANT` directly. That is the user's own activity, not a ferry shortcut. To change a fake transcript, keep its mtime over 2 seconds old (`$W backdate FILE`); ferry waits on a transcript written in the last 2 seconds.

The real-agent journey (actual `claude --resume` in Rex, the agent recalling the codeword) can't run in a world. `test/e2e-local.sh` covers it, using the real home on kant and a real Rex session. Run it only when the change touches launch, liveness, or agent internals, then run `test/e2e-local.sh clean`.

## Evidence

Everything lands in `~/.cache/ferry-verify/<world name>/` (override with `FERRY_VERIFY_EVIDENCE`), numbered in order: `NN-laptop-ferry-<args>.log` holds the command, full output, and `[exit N]`; `NN-screen-<label>.txt` holds pane captures; `NN-state-<label>.txt` holds snapshots. Cleanup leaves this directory alone.

A proof:

- runs the command a user would, through `world.sh ferry` or `tty`. Calling `ferry remote` ops directly, or copying files into place by hand, proves nothing.
- has a `state` snapshot before and after the command, so the change is visible, not only the final log.
- checks side effects on both sides, not just the step log: worktree contents and git status on the receiver, the transcript's `cwd` rewritten to the new home, the sender still holding its own transcript, the task record's owner, state, and generation.
- reports every sub-feature ID in the feature files you drove as verified, failed (with the log), or skipped (with the reason). A sub-feature you reached some other way than its file's `How to get to it` and `Driving` steps counts as skipped.
- logs any check you run by hand (`stat`, `cat`, `grep`) into the evidence directory, e.g. `ls -l $KWT/.env | tee -a $EVIDENCE/checks.txt`.

`--no-launch` skips only the Rex launch: ferry prints the `resume` line instead. Every other step, including hooks and the session install, runs normally.

## The landing check

Every change needs a passing `all` run before it lands. `bin/worktree land` refuses a commit whose tree no `world.sh all` run has passed, unless the commit changes only Markdown. Run it from the checkout:

```
.claude/skills/verify-ferry/world.sh all
```

It refuses to start while anything is uncommitted, untracked files included, since it stamps the committed tree, `HEAD^{tree}`. It then builds a new world, drives hand-off, bring-back, and remotes with no terminal, and checks both homes after each step. Each check prints `ok` or `FAIL` and goes into `<evidence>/all.txt`. When every check passes, it writes the tree to `<evidence>/verified`, the stamp `land` looks for, and cleans the world. When a check fails, it keeps the world and names it, and writes no stamp.

`all` doesn't read this machine's setup. It runs with a new empty `HOME`, so git reads no user config, and `GIT_CONFIG_NOSYSTEM` drops the system's. It also sets `GOENV=off` and an empty `SHELL`, so ferry never asks a login shell for `PATH`. Its `PATH` holds only the directories of `go`, `git`, `rsync`, `jq`, and `perl`, then the system's own. It carries over only Go's toolchain and caches and the evidence root. Since the user's global gitignore doesn't apply, a file only that ignore hides counts as uncommitted.

Run it last, after the feature files your change touches. A rebase changes the tree, so rebase first and run it again. It covers the journeys every change can break. It doesn't replace driving the features your change touches, or the prompts that need the tmux pane. The verify-handoff skill's TLA+ model check is separate and not part of landing.

## Cleanup

```
$W clean $R
```

It kills this world's private tmux server (`tmux -L <world name>`, which nothing else uses) and removes `$R`. It never touches the real home, Rex, or other worlds, and it leaves the evidence directory. Run it after failed attempts too. To find worlds you stranded: `ls -d /tmp/ferry-verify-*`; clean only the ones you created.

## Helpers

`world.sh`, used throughout above. Run it with no arguments for the full usage.
