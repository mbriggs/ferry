---
name: worktree
description: Work on ferry in a worktree lane. Use before the first file write in the primary checkout, for a parallel session or separate worktree, and when the working directory is under .claude/worktrees/.
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
- `make install` replaces `~/.local/bin/ferry`, the binary every real handoff runs. In a lane it asks for the branch name on the terminal, so it always refuses for an agent. In the primary, `bin/worktree sync` runs it after a merge.
- Don't run the lane's `./ferry setup`, `ferry to`, or `ferry from` against the real home or kant. They act on real state, and nothing guards them. Use the verify-ferry skill's fake homes.

## The working directory

The env hook loads `worktree.env` for the checkout a command starts in. `cd <lane> && cmd`, run from the primary, gets the primary's env. Run lane commands with the lane as the working directory.

## Commands that run in the primary

A session isolated to a worktree can't run git against the primary. Call `ExitWorktree` with `keep`, run the command from the primary, then re-enter the lane with `EnterWorktree` and its path.

## Landing

A lane lands as one commit, through a pull request that you own until it merges. Don't commit, merge, or push to the primary's `main` by hand. `bin/worktree land`, `merge`, and `sync` do those in the steps below.

1. In the lane, run `git fetch origin`, then `git rebase origin/main` if the lane is behind. `land` refuses a lane that's behind, because a rebase changes the tree that verification stamped. When `origin/main` changed `bin/worktree` or this skill, `land` says so. Reread this file after rebasing, because the copy you started from is stale.
2. With nothing uncommitted, run `.claude/skills/verify-ferry/world.sh all` in the lane (see verify-ferry's "The landing check"). `land` refuses a tree that no passing run stamped. A change to Markdown alone needs no run. For a change that can't affect what the ferry binary does, like one only to `bin/worktree` or `test/`, pass `--no-verify REASON` to `land` instead, and the reason goes in the PR.
3. Write the commit message you want on `main` to a file. Write the PR description to a second file, for a reviewer who never saw this session. Keep both outside the checkout, or `land` counts them as changes.
4. In the lane, run `bin/worktree land --message MSG --body DESC`. It squashes the lane into one commit with that message and pushes the branch. Then it opens the PR, or sets the open PR's title and description through the REST API. Pass both files on every land, including re-lands.
5. Watch CI on the lane's branch: run `/opt/hallpass/bin/hallpass gh pr checks BRANCH --watch` in the background, and it ends when every check has finished. If a check fails, read `gh run view RUN --log-failed`, fix the cause in the lane, and start again from step 1. Handle a review comment that asks for changes the same way, then reply to it. If you can't fix a failure, tell the user what failed.
6. When every check passes and no review asks for changes, merge the PR. The user asked for this in this repo, though merging pushes `main` and cuts a release. To merge, call `ExitWorktree` with `keep`, then run `bin/worktree merge NAME` in the primary. `merge` refuses unless the checks passed and the PR's head is the lane's HEAD. It sends one squash merge, pinned to that head. Don't retry it by hand. If the merge reply fails, `merge` reads the PR's state, and running `merge` again reports a merge that already happened. After merging, it runs `sync`.
7. `sync` fast-forwards the primary's `main` and runs `make install`, so `~/.local/bin/ferry` runs what merged. When the user merged the PR, run `bin/worktree sync` in the primary yourself. If `sync` prints `NOT INSTALLED`, tell the user to run `make install` in the primary. If `sync` prints `the ferry protocol went from`, tell the user to run `make push` in the primary. Until they do, every handoff to kant refuses.

To keep working in the lane after a squash merge, run `git rebase --onto origin/main <merged PR head>`. A plain rebase replays the squashed commits.

## Cleanup

Ask the user before you remove a lane. Call `ExitWorktree` with `keep`, never `remove`. `remove` refuses any worktree with commits made since it was created, even landed ones. Then run `bin/worktree teardown NAME` from the primary. It runs `sync` first, and then refuses a lane with uncommitted changes, or one whose HEAD is neither on `main` or `origin/main` nor the head of a merged pull request. `--discard` skips both checks, but only pass it after the user agrees to lose the work. Teardown removes the worktree, deletes the branch, and drops the lane.

Running teardown from inside the lane leaves the directory behind, and the next command there provisions the lane again.

Lanes whose directory is gone are dropped automatically at session start, after `ExitWorktree`, and 30 seconds after a subagent stops (`bin/worktree teardown --stale`; log in `tmp/worktree-sweep.log`).
