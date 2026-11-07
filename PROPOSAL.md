# ferry: hand a coding task and its agent session from the laptop to kant

## The short version

You start most work on the laptop: you poke at it with local resources, work out the details with the agent, and then hand it to kant so it can keep going after you close the lid. `ferry to kant` does that handoff. `ferry from kant` brings a task back when you want it local again.

`ferry` is a small Go binary. A task is one working tree plus one Claude Code or Codex session. `ferry` moves the code as git objects and the transcript as a rewritten JSONL file. It keeps one owner per task in a record on kant. Environment state like databases, dev servers, and installed dependencies doesn't move by itself. Each project gets two scripts for that: `dehydrate` on the machine giving up the task and `restore` on the machine taking it. A skill writes and maintains those scripts. Rex starts, watches, and stops the agents, and SSH over the tailnet carries the bytes.

Ten independent drafts with different starting points (smallest script, git-native, Rex-native, continuous mirror, safety-first, buy-before-build, and others) mostly produced this same shape. Eight picked the same temp-index git snapshot. Nine fence the old machine by moving its transcript out of the agent's search path. All ten rejected continuous two-way sync and Polygraph.

## Prerequisite: kant has no SSH server

`sshd` is inactive on kant and Tailscale SSH is off (`RunSSH: false`). Run this once:

```
sudo tailscale set --ssh
```

All `ferry` commands run on the laptop and connect to kant. kant never connects to the laptop, so the laptop needs no SSH server, and a closed lid can't interrupt a handoff.

## Commands

```
ferry to HOST [--session ID] [--no-restore]  hand the task in $PWD to HOST and start the agent there
ferry from HOST NAME [--wait]                 bring task NAME from HOST to this machine
ferry new NAME REPO [--agent claude|codex]    start a task directly on kant
ferry ls                                      tasks, owner, generation, agent state
ferry recover [NAME]                          finish or roll back an interrupted handoff
```

HOST is a tailnet name. With one peer configured, it's `kant` in every example here. The commands read as sentences on purpose: `ferry to kant` before you close the lid, `ferry from kant fix-auth` when you want a task back.

`ferry to` needs no setup. Running it in any directory under `~/src` creates the task the first time. That includes a repo kant has never seen (see "Repos kant doesn't have" below). `ferry new` is for the less common case of starting from scratch on kant.

## The main flow: laptop to kant

```
~/src/app-wt/fix-auth (fix-auth) $ ferry to kant
agent      claude 7c1e9a02, process exited, transcript quiet 40s
dehydrate  .ferry/dehydrate: dumped dev db (2.1 MB), stopped 1 dev server
children   none left running
snapshot   base 4be21f0, 1 staged, 3 modified, 2 untracked
extras     .env.local (from .ferry/include), .ferry/state/ (2 files)
versions   claude 2.1.319 here, 2.1.319 on kant
repo       kant: ~/src/app exists, same root commit
git        pushed refs/ferry/fix-auth/{base,index,wip} to kant:~/src/app
apply      kant: worktree ~/src/app-wt/fix-auth, tree matches snapshot
session    7c1e9a02 rewritten /Users/mbriggs -> /home/mbriggs, 1 subagent dir
owner      kant owns fix-auth (generation 1)
park       local transcript moved to ~/.local/state/ferry/parked/
restore    kant: .ferry/restore in rex pane "fix-auth:restore" ... ok (38s)
rex        kant: session "fix-auth" running claude --resume 7c1e9a02
done in 46s
```

Close the lid. The agent keeps working in a Rex session on kant, and you can attach to it from any client.

## Pulling a task back

```
~/src/app $ ferry from kant fix-auth
kant       claude 7c1e9a02 status idle in rex block fix-auth; sending /exit ... gone
dehydrate  kant: .ferry/dehydrate ok
snapshot   kant: base 81aa3c2 (3 new commits), 4 modified
git        fetched refs/ferry/fix-auth/*
apply      local worktree matches the snapshot it pushed in gen 1, applying
session    rewritten /home/mbriggs -> /Users/mbriggs
owner      laptop owns fix-auth (generation 3)
restore    .ferry/restore ... ok
resume:    cd ~/src/app-wt/fix-auth && claude --resume 7c1e9a02 "<resume note>"
```

If the agent is mid-turn, `ferry from` refuses. `--wait` polls until it goes idle.

## Repos kant doesn't have

All code lives in `~/src` on both machines, which keeps paths simple. A repo at `~/src/app` on the laptop always lives at `~/src/app` on kant. Worktrees keep the same position relative to `~/src`. `ferry` refuses a directory outside `~/src`.

If kant has no repo at that path, `ferry to` creates one:

1. `ssh kant git init ~/src/app`
2. `git push kant:~/src/app` with all local branches and tags. The laptop is the source, so this works for repos with no remote and for unpushed local branches.
3. Copy the `origin` URL onto kant's clone, so kant can fetch and push to origin later.
4. Continue with the normal snapshot transfer.

If `~/src/app` exists on kant but has a different root commit, `ferry` stops instead of guessing. It treats it as a different repo that happens to share a name.

A plain directory with no git falls back to `rsync -a --delete --backup-dir` into the same `~/src` path, with the same include and exclude rules.

## How code moves

The snapshot is three commits built with git plumbing. It doesn't touch the user's index, stash, or branch:

1. `base` is HEAD. Local commits that were never pushed to origin travel with it.
2. `index` is `git write-tree` of the real index, committed on `base`, so staged versus unstaged survives the trip.
3. `wip` is `git add -A` into a temporary `GIT_INDEX_FILE`, committed with `base` and `index` as parents. This is `git stash -u` without modifying anything.

`ferry` pushes all three to `refs/ferry/<task>/` in kant's clone, or fetches them from kant on `ferry from`. Only missing objects travel. Hidden refs never block a checked-out branch, and a normal push never sends them to origin.

On the receiver, `ferry` creates the worktree with `git worktree add` if it's missing, checks out `base`, restores the working tree from `wip`, and reads `index` back into the index. It then compares the result with the snapshot and stops on any mismatch.

The receiving tree must be clean, or identical to the snapshot it last sent. If you edited the copy you handed away, `ferry` stops and lists the files. `--branch` lands the incoming work on `ferry/<task>-<gen>` without touching your tree. Nothing merges automatically.

Ignored files stay behind unless they match `.ferry/include`, a gitignore-style list that defaults to `.env*`. `ferry` prints their names before sending and writes them with mode 0600. Dependency directories (`node_modules`, `_build`, `deps`, `target`, `.venv`) never move, because Mac and Linux builds differ. Rebuilding them is `restore`'s job.

## Project scripts: dehydrate and restore

The code and the conversation are generic. A project's environment isn't. One project needs a Postgres dump loaded, another needs `mix deps.get` and a Phoenix server, and another needs nothing. Each project gets two optional executables:

- `.ferry/dehydrate` runs on the machine giving up the task, after the agent stops and before the snapshot. It captures state that git doesn't hold into `.ferry/state/`, such as a database dump or a list of running services, and it stops anything that holds the working tree. Everything in `.ferry/state/` travels with the snapshot.
- `.ferry/restore` runs on the machine taking the task, after ownership moves and before the agent starts. It installs dependencies, loads what `dehydrate` saved, and starts services. It runs on kant, so it can start long-running services in their own panes with the local `rex run`.

`ferry` passes both scripts the same environment:

```
FERRY_DIRECTION=to|from   FERRY_TASK=fix-auth
FERRY_FROM=laptop  FERRY_TO=kant
FERRY_ROOT=<worktree path on this machine>
FERRY_STATE=<root>/.ferry/state
FERRY_FIRST=1                 first time this worktree exists on this machine
```

The contract has three rules:

- **Rerunnable.** `ferry recover` may run either script again, so running it twice has to end in the same state as running it once.
- **Exit status is the verdict.** If `dehydrate` exits nonzero, the handoff stops before anything moves. If `restore` exits nonzero, the task still belongs to the receiver, because the code and transcript arrived intact, but the agent doesn't start. `ferry recover` reruns `restore`.
- **A one-line summary on the last line of stdout.** `ferry` prints it and adds it to the agent's resume note, such as "restore: deps installed, db loaded from dump, server on :4000 in pane fix-auth:server."

Scripts live in the repo under `.ferry/` and are committed, so both machines get them through the snapshot. For repos where you don't want to commit them, such as a work repo, `ferry` also looks in `~/.config/ferry/projects/<repo>/`. `--no-restore` skips `restore` for one handoff.

The scripts are optional. A project without them still moves code and the session, and the resume note tells the agent the environment is cold.

### The ferry-scripts skill

Writing these scripts is the same job each time: read the project, find out what the environment needs, write two short scripts, and prove they work. A skill handles both building and maintaining them.

- **Build.** It reads the project's lockfiles, `mise.toml`, `docker-compose.yml`, `Procfile`, `.env.example`, database config, and README setup steps. It writes `dehydrate` and `restore` and keeps them short and idempotent. It asks where state should come from when it's ambiguous, for example dumping the database versus reseeding it.
- **Verify.** A script counts as working only after a real round trip. The skill pushes a scratch branch to kant, confirms `restore` brings the app up (tests pass, the server answers), brings it back with `ferry from`, and confirms the same on the laptop. Then it deletes the scratch task.
- **Maintain.** When `restore` fails, `ferry` prints the script's output and suggests running the skill. The skill reads the failure, diffs the project against what the scripts assume (a new service, a renamed lockfile, a new env var), and updates the scripts, then verifies again.

The skill doesn't need to be part of v1 of the binary. It should exist by the time two or three projects have scripts, because that's when they start drifting.

## How the agent session moves

**Is it safe to move?** A live agent holds turn state in memory, so `ferry` requires the agent to have exited. It checks three things:

- Claude Code keeps a registry of live processes at `~/.claude/sessions/<pid>.json`, with `sessionId`, `cwd`, and `status` (`busy` or idle). `ferry` reads this instead of scraping the screen.
- For Codex, `ferry` looks for a live `codex` process with this cwd, using `/proc` on Linux and `lsof` on macOS, and checks `~/.codex/thread-writer-locks`. The lock file format still needs confirming.
- The transcript hasn't been written in the last 15 seconds.

If the agent is idle in a Rex pane on kant, `ferry from` sends `/exit` with `rex send` and waits for the process to leave. On the laptop, `ferry to` asks you to exit the agent. It never kills a busy agent. After `dehydrate` runs, `ferry` lists any child processes the agent started that are still running and leaves them behind with a warning.

**What moves.**

- Claude Code: `~/.claude/projects/<slug>/<uuid>.jsonl`, the sibling `<uuid>/` directory (subagents), and `file-history/<uuid>/` (for rewind). Project `memory/` files are copied only if absent on the receiver. A file that differs on both sides keeps both copies.
- Codex: the rollout file at the same `sessions/YYYY/MM/DD/` relative path.
- Never: credentials, `auth.json`, `~/.claude.json`, or the `.key` files in `~/.claude/sessions/`. The first resume in a new directory may show Claude's trust prompt once.

**Paths.** Because everything is under `~/src`, the only difference between machines is the home prefix, `/Users/mbriggs` against `/home/mbriggs`. The rewrite parses each JSONL line and replaces string values equal to the old home or starting with `home + "/"`. That covers Claude's `cwd` and `workingDirectory` and Codex's `session_meta.cwd`, `runtime_workspace_roots`, and `turn_context.cwd`. A line that fails to parse aborts the handoff. The Claude slug directory name is recomputed from the new path.

Old tool output in the transcript still shows the other machine. So `ferry` resumes with a first prompt, which `claude --resume <id> "<msg>"` and `codex resume <id> "<msg>"` both accept:

> Resumed on kant. The working directory moved from /Users/mbriggs/src/app-wt/fix-auth to /home/mbriggs/src/app-wt/fix-auth. Uncommitted changes came along. restore: deps installed, db loaded from dump, server on :4000 in pane fix-auth:server. Nothing else from the laptop is running here.

**Versions.** If the receiver's agent is older than the sender's, `ferry` refuses unless you pass `--force-version`. A newer receiver is fine.

## One owner at a time

kant holds one record per task at `~/.local/state/ferry/tasks/<task>.json`: `{owner, generation, state, snapshot, session, rex_block}`. Every handoff goes through kant, so the record is reachable whenever a handoff can happen. Writes are a compare-and-swap on `generation` under `flock`, run over SSH. Every transition bumps the generation, so a stale or resurrected process can't commit an old handoff. The first `ferry to` for a task creates the record.

Fencing doesn't depend on anyone reading the record. After a handoff commits, the source moves its transcript out of `~/.claude/projects` or `~/.codex/sessions` into `~/.local/state/ferry/parked/`. A habitual `claude --resume` on the old machine then fails with "no session" instead of quietly forking the conversation. Parked copies stay for 14 days.

The record never expires on its own. A closed laptop is the normal case, and automatic takeover is how two machines end up running the same session. If the laptop is lost while it owns a task, kant can't recover what it never received, and `ferry` says so.

### Interrupted handoffs

Each handoff runs these steps in order. Each step is idempotent and leaves a marker:

1. Preflight: agent stopped, versions, divergence. Record `handing-off(A->B)` at generation g+1.
2. Run `dehydrate` on the source.
3. Create the repo on the receiver if it's missing. Snapshot, then push or fetch the refs and extras.
4. Copy the session into staging on the receiver (`~/.local/state/ferry/in/<task>/<gen>/`) and verify its digest.
5. Apply the tree and verify it.
6. Move the staged transcript into the live agent directory with a single rename, and record `owned(B)` at g+2. This is the commit point.
7. Park the source transcript, run `restore` on the receiver, then start the agent through Rex.

A failure before step 6 leaves the source as owner. The working tree and transcript there are untouched. Whatever `dehydrate` stopped can be brought back by running `restore` locally, which `ferry recover` does for you. A failure after step 6 leaves the receiver as owner, and `ferry recover` on either side finishes step 7. If kant is unreachable, `recover` refuses to guess.

## Where Rex fits

Rex runs the agents and keeps them alive on kant while the laptop is closed, which is what the whole setup is for. `ferry` uses `rex -S kant run` to start the resumed agent and the `restore` pane, `rex send` and `rex wait` to stop an idle agent, and it stores the block ID in the task record. An `init.lua` action can bind "ferry this pane to kant" to a key that calls `ferry to kant`. Without a Rex server, `ferry` prints the resume command.

Rex doesn't move bytes, and `ferry` doesn't ask it to. Its terminal API has `upload_begin`, but that's for files dropped onto a terminal, and the receiving terminal types their paths as pasted text. Two things worth asking Rex for:

- A session ID field in the program status escape sequence, so `ferry` can map a pane to its transcript directly.
- A host-to-host `rex cp`, which would remove the SSH prerequisite.

## Build versus reuse

Build. Reuse git for the hard parts, and read the prior art for edge cases.

| Tool | Verdict | Why |
|---|---|---|
| git | Use | Delta transport, integrity, and history over SSH. The snapshot is about 40 lines of plumbing. |
| cct | Read, don't depend | Covers both agents and has a useful `import --merge`, but it was last tested against older Claude and Codex versions than kant runs, and it only rewrites cwd. |
| entangle | Read | Best re-pathing rules and a list of the portable `~/.claude.json` keys. Its live transfer goes through public wormhole servers. Claude only. |
| agent-sync | Read | Good `lsof` writer check. Doesn't rewrite cwd, so `/Users` to `/home` breaks. |
| mutagen, Syncthing, gut | No | Continuous two-way sync is the opposite of one owner. mutagen hasn't shipped a release since Feb 2025. |
| tailscale file, magic-wormhole | No | Taildrop is alpha and needs root to receive on Linux. Wormhole goes through a public rendezvous server. |
| Polygraph | No | A commercial team product for cross-repo dependency graphs and shared session memory. It doesn't move a working tree or transcript between two of your machines, and it adds a service. |

None of the session movers moves code or holds a lock. Pairing one with a code syncer gives you two tools that can disagree about who owns a task.

## v1 scope and size

In scope: `to`, `from`, `new`, `ls`, `recover`. Claude Code and Codex. Git checkouts, linked worktrees, and plain directories under `~/src`. Creating a missing repo on kant. `.ferry/include`. Running `dehydrate` and `restore`. The ownership record and fencing on kant. Rex launch and stop, with a printed fallback.

Size: one Go binary for macOS and Linux, about 1,200 lines:

| Part | Lines |
|---|---|
| Snapshot, apply, divergence check, repo creation | 350 |
| Session detection, rewrite, parking | 300 |
| Ownership record, steps, recover | 300 |
| Project script runner | 80 |
| SSH, Rex, process checks | 170 |

Add about 400 lines of tests. The one that matters kills the process after each of the seven steps and checks that `recover` ends with exactly one owner and no lost files. That's about a week to working, then a week of daily use before I'd trust it. The ferry-scripts skill comes after, built against the first two or three real projects.

Test these first, because the rest depends on them:

1. Copy one Codex rollout to a new date path and resume it. Codex also keeps `state_5.sqlite` and `thread_history_1.sqlite`, which may index rollouts.
2. Rewrite a real Claude transcript from `/Users` to `/home` and resume it with `--resume`. Check that subagents and rewind still work.
3. Confirm what `~/.codex/thread-writer-locks` contains while Codex is running.

Left out on purpose:

- Moving an agent mid-turn. `ferry` waits for idle.
- Continuous or automatic sync, including handing off when the lid closes.
- Merging diverged trees or transcripts.
- `~/.claude.json`, MCP config, Codex SQLite state, credentials.
- Submodules and git-lfs.
- Directories outside `~/src`.
- A third machine. The record has an owner field, so it's an extension, not a redesign.
- Background checkpoints to kant while the laptop owns a task. These would make a lost laptop recoverable, and they're the first thing I'd add.
