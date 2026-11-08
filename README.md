# ferry

Hand a coding task, meaning a git worktree under `~/src` plus its Claude Code or Codex session, from the laptop to kant and back. The agent resumes the full conversation on the other side. See [PROPOSAL.md](PROPOSAL.md) for the design.

```
ferry to kant                 # in the worktree, after exiting the agent
ferry from kant fix-auth      # bring it back
ferry from kant               # pick from the agents running on kant
ferry ls kant
```

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

On a terminal, ferry colors its step labels, wraps long lines, shows progress for big transfers, and asks with arrow-key prompts. A single `y` or `n` answers most of them; a prompt that would run code (a hook, or Claude after its config changed) ignores letters and takes an arrow key and Enter, so text typed ahead can only answer No. Piped output, or `FERRY_PLAIN=1` (or `NO_COLOR`), gives plain lines and `[y/N]` prompts for scripts and screen readers.

## First real test

Use a scratch repo:

1. On the laptop, run `mkdir -p ~/src/ferry-trial && cd ~/src/ferry-trial && git init`, commit one file, and leave one uncommitted.
2. Run `claude`, tell it a codeword, then `/exit`.
3. Run `ferry to kant`. It should finish with `launch  kant: rex session "ferry-trial" running claude --resume ...`.
4. Attach to that Rex session on kant and ask for the codeword.
5. Run `ferry from kant ferry-trial` on the laptop. When it finishes, the session resumes in the same terminal.

## Tests

- `make test` runs a round trip between two fake homes. The remote side runs as a real `ferry remote` subprocess.
- `test/e2e-local.sh` runs a real Claude session on kant through Rex, with a fake laptop home. Run `test/e2e-local.sh clean` afterwards.
- `test/worktree.sh` tests `bin/worktree`, which gives each linked git worktree its own lane of test-script names. See `.claude/skills/worktree/SKILL.md`.
- `test/liveness.sh` rechecks the agent internals ferry depends on. Run it after upgrading Claude Code or Codex.

## CI and releases

`.github/workflows/ci.yml` runs gofmt, `go vet`, the test suite and `make dist` on every push to a branch other than main, on both Linux and macOS, since the laptop side runs on a Mac. A push to main (a merge) runs the same checks, then `release.yml` tags the next patch version and publishes a GitHub release with the four binaries and `SHA256SUMS`. The first release is v0.1.0. To bump the minor or major version, push a tag like `v0.2.0` by hand, and later releases count up from it.
