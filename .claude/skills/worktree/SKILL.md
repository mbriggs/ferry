---
name: worktree
description: Work on ferry in a git worktree lane beside the primary checkout. Use before the first file write in the primary checkout, when the user asks for a parallel session or a separate worktree, and whenever the working directory is under .claude/worktrees/.
---

# Working in a lane

A lane is a linked git worktree of ferry with its own names for everything the test scripts create. It has its own `./ferry` binary and its own `/tmp/ferry-*-lane-<n>` scratch directories. Its e2e probe is `~/src/ferry-e2e-probe-lane-<n>`, with that name's task record, and its Rex sessions are named `ferry-*-lane-<n>`. The primary checkout keeps the plain names. `bin/worktree info` prints the names the current checkout uses.

Make every change in a lane, not in the primary checkout.

## Creating a lane

- In this session, call `EnterWorktree`.
- For a subagent, pass `isolation: "worktree"`. Its worktree branches from the current commit (`worktree.baseRef: "head"`), so it has your unpushed commits but not your uncommitted edits.
- From a terminal, run `claude --worktree NAME`.
- Anything else: `git worktree add .claude/worktrees/NAME -b NAME`, then `bin/worktree setup` inside it.

## Provisioning

The first Bash command in a new worktree runs `bin/worktree setup`, which registers the lane, runs `make build`, and writes `worktree.env`. The hook then reports the lane's names. If setup fails, that one command is blocked and `tmp/worktree-setup.failed` holds the log. Read it, fix the cause (usually a build error at this commit), and run `bin/worktree setup` again. Until it succeeds, every script in `test/` that sources `test/lib/lane.sh` refuses to start.

## Commands in a lane

- `make build`, `make test`, `go test ./...`, `make dist`, `formal/check.sh`, and the verify-ferry `world.sh` already write only inside the checkout or under `mktemp` names. They need nothing from the lane.
- `test/e2e-local.sh`, `test/claude-resume.sh`, `test/codex-*.sh`, `test/git-snapshot.sh`, and `test/liveness.sh` source `test/lib/lane.sh` and use the lane's suffixed names. Teardown kills their Rex sessions by label and deletes what they left.
- `make install` replaces `~/.local/bin/ferry`, the binary every real handoff runs. In a lane it asks for the branch name on the terminal, so it always refuses for an agent. Send the user to the primary.
- Don't run the lane's `./ferry setup`, `ferry to`, or `ferry from` against the real home or kant. They act on real state, and nothing guards them. Use the verify-ferry skill's fake homes.

## The working directory

The env hook loads `worktree.env` for the checkout a command starts in. `cd <lane> && cmd`, run from the primary, gets the primary's env. Run lane commands with the lane as the working directory.

## Commands that run in the primary

A session isolated to a worktree can't run git against the primary. Call `ExitWorktree` with `keep`, run the command from the primary, then re-enter the lane with `EnterWorktree` and its path.

## Landing

A lane lands as a pull request, never directly on `main`. Commit in the lane, then run `bin/worktree land` in the lane (or `bin/worktree land NAME` from the primary, with a name under `.claude/worktrees/` or a path). It refuses if the lane has uncommitted tracked changes or no `origin` remote. Then it rebases the lane onto `origin/main`, pushes the lane's branch, and opens its pull request, or prints the URL of the one already open. On a rebase conflict it stops; resolve the conflict in the lane and run `land` again. Running `land` again after more commits pushes them to the same pull request.

`land` fills in the pull request from the commits. If the lane has more than one commit, or the commits don't explain the change on their own, rewrite the body with the `pr` skill (`gh pr edit --body-file`).

Never commit to the primary's `main`, merge into it, or push it. The user merges the pull request; that push to `main` cuts a release.

## Cleanup

Ask the user before you remove a lane. Call `ExitWorktree` with `keep`, never `remove`. `remove` refuses any worktree with commits made since it was created, even landed ones. Then run `bin/worktree teardown NAME` from the primary. It refuses a lane with uncommitted changes, or one whose HEAD is neither on `main` or `origin/main` nor the head of a merged pull request. `--discard` skips both checks, but only pass it after the user agrees to lose the work. Teardown removes the worktree, deletes the branch, and drops the lane.

Running teardown from inside the lane leaves the directory behind, and the next command there provisions the lane again.

Lanes whose directory is gone are dropped automatically at session start, after `ExitWorktree`, and 30 seconds after a subagent stops (`bin/worktree teardown --stale`; log in `tmp/worktree-sweep.log`).
