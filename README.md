# ferry

Hand a coding task, meaning a git worktree under `~/src` plus its Claude Code or Codex session, from the laptop to kant and back. The agent resumes the full conversation on the other side.

```
ferry to kant                 # in the worktree, after exiting the agent
ferry to kant fix-auth        # in the repo's main checkout: send its worktree fix-auth
ferry from kant fix-auth      # bring it back
ferry from kant               # pick from the agents running on kant
ferry ls kant
```

## Worktrees Claude made

A worktree from `claude --worktree NAME` or the EnterWorktree tool belongs to Claude: when its session ends with `/exit`, Claude deletes the worktree if it has no changes and no commits, and otherwise asks Keep or Remove. Keep moves the transcript into the main checkout's project folder. ferry finds the session either way, resumes it in the worktree, and sends it as a plain session, so its next `/exit` on the other machine leaves the worktree alone.

To hand one off:

- Say "ferry this to kant" in the session. The `ferry` skill (`skills/ferry/`, installed to `~/.claude/skills/ferry/` by `make install`) opens a Rex tab that waits for the reply to end, stops the session with SIGTERM, which skips Claude's worktree cleanup, and runs `ferry to kant` there.
- Or close the session's pane (or `/exit` and pick Keep), then run `ferry to kant` in the worktree, or `ferry to kant` in the main checkout, which lists the worktrees that have a session and asks which one to send.

Don't `/exit` a session whose worktree has no changes and no commits: that deletes the worktree. ferry releases the git lock a stopped Claude leaves on its worktree.

## Setup

1. On kant, enable SSH over the tailnet:

   ```
   sudo tailscale set --ssh
   ```

   Then give the tailnet policy an ssh rule that lets your laptop in as your kant user with `"action": "accept"`. A `"check"` rule waits for a browser login, which ferry's non-interactive ssh can't do. If kant has tags, the rule's `dst` must name the tag; `autogroup:self` only matches untagged devices.

2. Install ferry on the laptop: `make install` in this repo (or copy the release binary to `~/.local/bin/ferry`). Both machines need rsync 3.2.5 or newer on PATH. macOS's `/usr/bin/rsync` is openrsync, so on the Mac run `brew install rsync`. On kant, ssh runs rsync with a plain PATH, so it has to be in `/usr/bin` or `/usr/local/bin`.

3. From the laptop, connect it to kant. Your Mac username differs from kant's, so name the user:

   ```
   ferry setup kant mbriggs@kant
   ```

   This writes `~/.config/ferry/hosts`, checks ssh, installs this build of ferry on kant at `~/.local/bin/ferry` (from `dist/`, so run `make dist` first, or run setup from a release download), and checks the pair. Both machines must speak the same `ferry remote` protocol; with an older ferry on kant, every command stops before doing anything and says to run `ferry setup kant`.

   After changing or upgrading ferry, `ferry push` puts this build on every host in the hosts file that runs a different one (`ferry push kant` for one; `--dry-run` shows what would change). It refuses a binary in `dist/` built from a different commit than the running ferry, and checks that each installed copy answers with this version. In the ferry repo, `make push` rebuilds for every platform first, which also covers uncommitted changes, which the commit check can't see.

4. `ferry doctor` rechecks everything any time: both machines' ferry builds, names, ssh, the agents each side can find (kant's through its login shell's PATH), Rex, and the task records. Each failure comes with a fix.

Every ssh ferry starts, git's and rsync's included, shares one connection per handoff (ControlMaster, with sockets in `~/.local/state/ferry/ssh/`, kept for 60 seconds) and sends keepalives, so a dead link fails within about a minute instead of hanging.

On a terminal, ferry marks each step with a colored glyph (✓ done, · note, ! needs you, ✗ failed, ? asking, ◆ the commit point), dims details like shas and timings, wraps long lines, shows progress for big transfers, and asks with arrow-key prompts. A single `y` or `n` answers most of them; a prompt that would run code (a hook, or Claude after its config changed) ignores letters and takes an arrow key and Enter, so text typed ahead can only answer No. Piped output, or `FERRY_PLAIN=1` (or `NO_COLOR`), gives plain lines and `[y/N]` prompts for scripts and screen readers.

## How a handoff works

The laptop drives every handoff, and kant keeps one record per task in `~/.local/state/ferry/tasks/`, naming the machine that owns it. Each write to the record is a compare-and-swap on its generation. The steps run in this order:

1. Preflight. The agent must have exited. On the laptop you exit it yourself. On kant ferry stops an idle agent: Claude with SIGTERM, so a draft left in its input box is never sent, and Codex by typing `/exit` in its Rex pane. It never stops a busy one. If the receiver's agent is older than the sender's, ferry refuses unless you pass `--force-version`. Ferry also asks the receiver what the handoff would overwrite: uncommitted changes in its copy of the worktree, or commits there or on the branch that the sender's branch doesn't contain. At a terminal it offers to save that work to a new `ferry-saved/<task>-<time>` branch on the receiver first. Otherwise it stops, and `--force` overwrites without saving. `ferry to HOST --check` runs all of these checks while the agent still runs, and stops before it stops the agent or touches either worktree.
2. `.ferry/dehydrate` runs on the sender, if approved.
3. Code moves through git. The sender commits HEAD, its index, and its working tree as three hidden commits under `refs/ferry/<task>/`, without touching your index, stash, or branch. The receiver checks them out and stops if the result doesn't match the sender's fingerprint, or if its copy changed since it last handed the task away. Nothing merges. Claude's worktrees under `.claude/worktrees/` stay out of the snapshot. Any other untracked git checkout inside the worktree stops the handoff on the sender, before the receiver changes, because git would store it as a pointer to a commit only that checkout has.

   Remotes go with the code, and ferry never contacts them itself. The receiver gets every remote the sender has, with its URLs and push URLs, and the branch's upstream (`branch.<name>.remote` and `.merge`). It keeps any remote the sender lacks. Each remote-tracking ref (`origin/main` and the like) moves forward to the sender's when that remote has the same URLs on both sides. A ref that is newer on the receiver, or has diverged from the sender's, stays, so whichever machine fetched last wins. A repo ferry just created takes the config without asking. In an existing repo, ferry lists what would change and asks, because a new URL or upstream changes where the next push goes. `--yes` answers going to kant but not coming to the laptop, for the same reason hooks from kant ask on every run. If you say no, the receiver's config stays as it was.
4. Ignored files move with rsync: `.env` files, generated output, `.claude/`, and `.ferry/state/`, which never becomes a git object. Dependency and build trees (`node_modules`, `target`, `.venv` and the like) stay, as do Claude's worktrees under `.claude/worktrees/` and anything `.ferry/exclude` names. Symlinks stay too, since one from the other machine could point anywhere in your home. Ferry lists what stays, and asks before sending what changed (`--yes` answers). Files arrive with only their owner's permission bits, so a `.env` lands 0600 and a script 0700. If the receiver changed an ignored file since it last sent the task, its copy is parked in `~/.local/state/ferry/parked/` before the sender's replaces it, and ferry says so. An ignored file the other side deleted is parked, not deleted, unless it changed here since it was sent. If ferry dies mid-copy, the task stays locked until its rsync exits, so a rerun can't start under an old copy that's still writing. Git hooks travel too. Coming to the laptop, they install only with a yes on that run.
5. The session and the agent's project memory are staged on the receiver with the home path rewritten, so nothing live has moved yet.
6. The record names the receiver as owner. This is the commit point. A failure before it leaves the sender as it was, and a failure after it leaves the receiver owning the task. Rerunning the same command picks up where it stopped, including a launch that failed.
7. The staged session moves into place, `.ferry/restore` runs on the receiver if approved, and the agent resumes. On kant it starts in a new Rex session. On the laptop it starts in your terminal, with one exception. Claude runs project hooks and `.mcp.json` servers as it starts, and `.claude/` travels, so before a pull touches anything ferry fingerprints what Claude would load: `.claude/`, `.mcp.json`, the project files named anywhere in Claude's project settings or `.mcp.json` (hooks, `statusLine`, `apiKeyHelper` and the rest), and the files those scripts name in turn. A name counts relative to the project, to the naming script's folder, or to any folder the same command names, which covers `cd tools && ./run.sh`. If the handoff changed any of it, ferry lists the files and starts Claude only with a yes on that run. The check finds files by name, so code a hook reaches without naming a file, like the Makefile behind `make check`, isn't covered.

The sender keeps its copy of the session. Resuming both forks the conversation, and the two copies take the Remote Control link from each other. If the receiver already had a copy from an earlier trip, that copy goes to `~/.local/state/ferry/parked/` first, so turns you added there aren't lost. The record never expires on its own, because a closed laptop is the normal case.

### Project scripts

A project can have two optional executables. `.ferry/dehydrate` runs on the machine giving up the task, after the agent stops and before the snapshot. It saves state git doesn't hold into `.ferry/state/` and stops anything holding the tree. `.ferry/restore` runs on the machine taking the task, before the agent starts. It installs dependencies, loads what dehydrate saved, and starts services. Both get `FERRY_DIRECTION` (`to` or `from`), `FERRY_TASK`, `FERRY_ROOT` and `FERRY_STATE`, and both must be safe to run twice. If dehydrate fails, nothing moves. If restore fails, the task still moves but the agent doesn't start. The last line of stdout is a summary that goes into the agent's resume note.

kant runs agents unattended, so code from kant is trusted less. A script runs only once you approve its hash, which ferry keeps in `~/.config/ferry/approved`, and ferry shows the whole script first. Scripts over 64 KB never run. On the laptop, a script that may run code kant wrote asks on every run, even if approved: restore during `ferry from`, and dehydrate once the task has been on kant. Before it asks, ferry lists what changed since the laptop last sent the task, worked out from the laptop's own `refs/ferry-sent/<task>`, and flags build files like `package.json`, lockfiles and `Makefile`. `--yes` never approves a script. Without a terminal, a script that would ask stops the handoff instead of being skipped unasked; `--skip-scripts` skips it on purpose. Approval covers the script's own bytes, so a script whose `#!` line names a file in your home, or a relative path like `#!./tools/run`, never runs: that file could change without the script changing. Use `#!/bin/sh` or `#!/usr/bin/env NAME`. ferry doesn't sandbox anything. Once you say yes, kant's changes run with your permissions.

## First real test

Use a scratch repo:

1. On the laptop, run `mkdir -p ~/src/ferry-trial && cd ~/src/ferry-trial && git init`, commit one file, and leave one uncommitted.
2. Run `claude`, tell it a codeword, then `/exit`.
3. Run `ferry to kant`. It should finish with `launch  kant: rex session "ferry-trial" running claude --resume ...`.
4. Attach to that Rex session on kant and ask for the codeword.
5. Run `ferry from kant ferry-trial` on the laptop. When it finishes, the session resumes in the same terminal.

## Tests

- `make test` runs a round trip between two fake homes. The remote side runs as a real `ferry remote` subprocess.
- `make cover` writes coverage to `tmp/cover/`. It counts the ferry subprocesses too, since `TestMain` builds the binary with `-cover` whenever the tests run under coverage.
- `bin/mutate FILE.go` runs [gremlins](https://gremlins.dev) on one source file. It changes one condition or operator at a time, runs the suite against each change, and lists the changes no test caught. `handoff.go` takes about an hour. Most survivors change only output, or can't change behavior, so read each one before writing a test for it.
- `test/e2e-local.sh` runs a real Claude session on kant through Rex, with a fake laptop home. Run `test/e2e-local.sh clean` afterwards.
- `test/worktree.sh` tests `bin/worktree.lane`, which tells the shared `bin/worktree` script what each linked git worktree's lane gets: its own binary and its own test-script names. See `.claude/skills/worktree/SKILL.md`.
- `test/liveness.sh` rechecks the agent internals ferry depends on. Run it after upgrading Claude Code or Codex.
- `test/claude-worktree.sh` rechecks what Claude does when a worktree session ends (see "Worktrees Claude made"). Run it after upgrading Claude Code.

## CI and releases

`.github/workflows/ci.yml` runs gofmt, `go vet`, the test suite and `make dist` on every push to a branch other than main, on both Linux and macOS, since the laptop side runs on a Mac. A push to main (a merge) runs the same checks, then `release.yml` tags the next patch version and publishes a GitHub release with the four binaries and `SHA256SUMS`. The first release is v0.1.0. To bump the minor or major version, push a tag like `v0.2.0` by hand, and later releases count up from it.
