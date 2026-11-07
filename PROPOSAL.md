# ferry: hand a coding task and its agent session from the laptop to kant

## The short version

You start most work on the laptop: you poke at it with local resources, work out the details with the agent, and then hand it to kant so it can keep going after you close the lid. `ferry to kant` does that handoff. `ferry from kant` brings a task back when you want it local again.

`ferry` is a small Go binary installed on both machines. A task is one git worktree under `~/src` plus one Claude Code or Codex session. `ferry` moves the code as git objects and the transcript as a JSONL file with its home-directory paths rewritten, then resumes the full session on the other side. It keeps one owner per task in a record on kant and fences the old copy so it can't be resumed by accident. Environment state like databases, dev servers, and installed dependencies moves through two per-project scripts, `dehydrate` and `restore`, which only run after you approve them. Rex starts, watches, and stops the agents. SSH over the tailnet carries the bytes.

This shape came out of ten competing drafts, then went through a four-judge review that rejected the first version on security, unverified assumptions, and cost. Prototypes in `test/` then checked the assumptions on kant (see "What the prototypes showed"). This version folds in both.

## Prerequisites

`sshd` is inactive on kant and Tailscale SSH is off (`RunSSH: false`). Run this once on kant:

```
sudo tailscale set --ssh
```

Lock it down with a tailnet ACL so only your own devices can SSH to kant as your user. Install `ferry` on both machines (`make install` builds for the local OS, `make dist` cross-builds the macOS binary).

All `ferry` commands run on the laptop and connect to kant. kant never connects to the laptop, so the laptop needs no SSH server. The laptop drives every step, though, so closing the lid in the middle of a handoff suspends it. Every step is idempotent, and running the same command again finishes the job.

## Commands

```
ferry to HOST [--agent claude|codex] [--session ID] [--yes] [--no-launch]   hand the task in $PWD to HOST
ferry from HOST NAME [--yes]                                                bring task NAME back here
ferry ls HOST                                                               tasks, owner, generation
ferry setup                                                                 add ferry's Claude Code permission rules
```

HOST is a name from `~/.config/ferry/hosts` (one `name ssh-target` pair per line, such as `kant mbriggs@kant`), or an SSH target used as is. The commands read as sentences on purpose: `ferry to kant` before you close the lid, `ferry from kant fix-auth` when you want a task back.

`ferry to` needs no setup. Running it in any git worktree under `~/src` creates the task the first time, including for a repo kant has never seen.

## The main flow: laptop to kant

```
~/src/app-wt/fix-auth (fix-auth) $ ferry to kant
task       fix-auth  (src/app-wt/fix-auth, repo src/app)
agent      claude 7c1e9a02, not running
versions   claude 2.1.319 here, 2.1.319 on kant
owner      new task, generation 1
dehydrate  .ferry/dehydrate (approved, sha 3f1c…): dumped dev db
repo       kant: src/app exists, same root commit
snapshot   base 4be21f0, wip 9d03c55
git        pushed refs/ferry/fix-auth/* to kant
extras     .env.local, .ferry/state/db.dump (approved)
session    7c1e9a02: 2 files, 1.9 MB
apply      kant: src/app-wt/fix-auth matches the snapshot
commit     kant owns fix-auth (generation 2)
park       local transcript moved to ~/.local/state/ferry/parked/
restore    kant: .ferry/restore (approved, sha 81aa…): deps installed, db loaded
launch     kant: rex session "fix-auth" running claude --resume 7c1e9a02
```

Close the lid. The agent keeps working in a Rex session on kant, and you can attach to it from any client.

## Pulling a task back

```
~/src/app $ ferry from kant fix-auth
agent      kant: claude 7c1e9a02 idle in rex, sending /exit ... stopped
snapshot   kant: base 81aa3c2 (3 new commits)
git        fetched refs/ferry/fix-auth/*
apply      local worktree matches the snapshot it sent in generation 2, applying
commit     laptop owns fix-auth (generation 4)
resume:    cd ~/src/app-wt/fix-auth && claude --resume 7c1e9a02 "<resume note>"
```

If the agent on kant is mid-turn, `ferry from` refuses and tells you to wait.

## Repos kant doesn't have

All code lives in `~/src` on both machines. A repo at `~/src/app` on the laptop always lives at `~/src/app` on kant, and worktrees keep the same position relative to `~/src`. `ferry` refuses directories outside `~/src`.

If kant has no repo at that path, `ferry to` creates one. It runs `git init` on a placeholder initial branch, pushes every local branch and tag, checks out the default branch, and copies the `origin` URL. The placeholder matters: git refuses a push to the branch a repository has checked out, even when that branch is still unborn. If the path exists with a different root commit, `ferry` stops instead of guessing.

## How code moves

The snapshot is three commits built with git plumbing. It doesn't touch the user's index, stash, or branch:

1. `base` is HEAD. Local commits that were never pushed to origin travel with it.
2. `index` is `git write-tree` of the real index, committed on `base`, so staged versus unstaged survives the trip.
3. `wip` is `git add -A` into a temporary `GIT_INDEX_FILE`, committed with `base` and `index` as parents. This is `git stash -u` without modifying anything.

The source writes these as `refs/ferry/<task>/{base,index,wip}` in its own repo, and the transfer is a plain push or fetch of `refs/ferry/<task>/*`. Hidden refs never block a checked-out branch, and a normal push never sends them to origin.

On the receiver, `ferry` creates the worktree with `git worktree add -B <branch>` at `base`, restores the working tree from `wip`, and reads `index` back into the index. It then fingerprints the result (index tree, full worktree tree, HEAD) and stops on any mismatch with the sender's fingerprint.

The receiving tree must not exist, or must still match the fingerprint recorded when it last handed the task away. If you edited the copy you handed away, `ferry` stops and lists the files. Nothing merges automatically.

`.ferry/state/` is excluded from the snapshot even if it isn't gitignored, so database dumps never become git objects.

## Secrets and other ignored files

Ignored files stay behind unless they match `.ferry/include`, one path pattern per line (default `.env*`). Everything in `.ferry/state/` travels too. These files go as a tar stream over SSH, never through git. `ferry` lists each file and its size and asks before sending (`--yes` skips the prompt), and writes them with mode 0600 on the other side. Dependency directories never move, because Mac and Linux builds differ. Rebuilding them is `restore`'s job.

## Project scripts: dehydrate and restore

Each project can have two optional executables:

- `.ferry/dehydrate` runs on the machine giving up the task, after the agent stops and before the snapshot. It writes state that git doesn't hold into `.ferry/state/` and stops anything holding the working tree.
- `.ferry/restore` runs on the machine taking the task, after ownership moves and before the agent starts. It installs dependencies, loads what `dehydrate` saved, and starts services. On kant it can start long-running services in their own panes with the local `rex run`.

Scripts never run without approval. The scripts come from the repo, and the repo is something an agent on the other machine can edit. Without approval, an agent on kant could plant code in `restore` that your laptop runs on the next `ferry from`. So `ferry` keeps approved script hashes in `~/.config/ferry/approved` on the machine that will run each script. A script whose hash isn't approved is shown in full on the laptop, and `ferry` asks before running it. `--yes` does not approve scripts. If you decline, the handoff continues without that step, and the resume note says the environment is cold.

`ferry` passes both scripts `FERRY_DIRECTION` (`to` or `from`), `FERRY_TASK`, `FERRY_ROOT` and `FERRY_STATE`. The contract has three rules:

- **Rerunnable.** Rerunning a handoff may run either script again, so running it twice has to end in the same state as running it once.
- **Exit status is the verdict.** If `dehydrate` fails, the handoff stops before anything moves. If `restore` fails, the task still belongs to the receiver, because the code and transcript arrived intact, but the agent doesn't start.
- **A one-line summary on the last line of stdout.** `ferry` prints it and adds it to the resume note.

### The ferry-scripts skill

A skill writes and maintains these scripts. It reads the project's lockfiles, `mise.toml`, compose files, `Procfile`, `.env.example` and README setup steps, and writes short idempotent scripts. A script only counts as working after a real round trip on a scratch branch. When `restore` fails, `ferry` suggests running the skill, which compares the project against what the scripts assume and updates them. It comes after the binary, once two or three projects have scripts.

## How the agent session moves

**Is it live?** A live agent holds turn state in memory, so `ferry` requires the agent to have exited:

- Claude Code keeps a registry of live processes at `~/.claude/sessions/<pid>.json`, with `sessionId`, `cwd` and `status` (`idle` or `busy`). `/exit` removes the file. A file whose pid is dead means a crash, and `ferry` ignores it.
- Codex holds an exclusive `flock` on `~/.codex/thread-writer-locks/<thread>.lock` while a thread is loaded, whether the TUI or the app-server daemon hosts it. `ferry` probes with a non-blocking shared lock. Under the daemon the lock lingers about 45 seconds after you quit. So `ferry` launches Codex with `--no-daemon`, where the TUI holds the lock and releases it the moment it exits.
- Rex's `program_status` reports `working`, `idle` or `done` for an agent pane. It's the second signal on kant.

On kant, `ferry from` stops an idle agent in its Rex pane with `/exit`, retrying because Codex can drop input that lands as a turn finishes. On the laptop, `ferry to` asks you to exit the agent. It never kills a busy agent.

**What moves.**

- Claude Code: `~/.claude/projects/<slug>/<uuid>.jsonl`, the sibling `<uuid>/` directory (subagents), and `~/.claude/file-history/<uuid>/` (for rewind).
- Codex: the rollout file at the same `sessions/YYYY/MM/DD/` relative path. Codex rebuilds its SQLite state from the rollout on the receiver, and catches its stale copy up after a round trip.
- Never: credentials, `auth.json`, `~/.claude.json` beyond the trust flag below, project `memory/` files, or the `.key` files in `~/.claude/sessions/`. Memory files are loaded as instructions in future sessions, so syncing them would let one machine plant instructions on the other.

**Paths.** The home prefix is the only difference between machines: `/Users/matt` on the laptop, `/home/mbriggs` on kant. The receiver rewrites every JSON string value that equals the sender's home or starts with `home + "/"`. Free text, like a shell command inside a tool call, keeps the old path. The Claude slug directory name is recomputed from the new path.

**Resuming.** `ferry` resumes with a first prompt that says what moved: the old and new working directories, that uncommitted changes came along, the `restore` summary, and that nothing else from the other machine is running here. Codex resumes with `-c model=<recorded model>`, because otherwise it silently switches to the configured default.

**Trust.** A resumed agent in a folder the receiver has never trusted stops at the trust prompt, and Claude's default answer there is "No, exit". `ferry` carries trust over only if you trusted the folder on the sending machine, and it does it the way you would: after launching in Rex, it answers the prompt in the pane (Down then Enter for Claude, Enter for Codex). Codex ignores trust passed with `-c`, and editing `~/.claude.json` or the stowed `config.toml` behind a running agent's back is riskier than answering the question it asks.

**Versions.** If the receiver's agent is older than the sender's, `ferry` refuses unless you pass `--force-version`.

## One owner at a time

kant holds one record per task at `~/.local/state/ferry/tasks/<task>.json`: `{owner, generation, state, agent, session, fingerprint}`. Every handoff goes through kant, so the record is reachable whenever a handoff can happen. Each write is a compare-and-swap on `generation` under `flock`, and every transition bumps the generation.

Fencing doesn't depend on anyone reading the record. After a handoff commits, the source moves its transcript into `~/.local/state/ferry/parked/`. A habitual `claude --resume` on the old machine then fails with "no session" instead of quietly forking the conversation. This also matters because every Claude resume re-links the session to its Remote Control cloud session, so a duplicate would fight over that link.

The record never expires on its own. A closed laptop is the normal case, and automatic takeover is how two machines end up running the same session.

### Steps

1. Preflight: task, agent stopped, versions. Record `handing-off` at generation g+1, or continue one this direction already started.
2. Run `dehydrate` on the source, if approved.
3. Create the repo on the receiver if missing. Snapshot, then push or fetch the refs.
4. Send extras and session files into a staging directory on the receiver.
5. Apply and verify the tree.
6. Move the session into the live agent directories, and record `owned` at g+2. This is the commit point.
7. Park the source transcript, run `restore` on the receiver if approved, and launch the agent.

A failure before step 6 leaves the source as owner with its transcript in place. A failure after step 6 leaves the receiver as owner. Either way, rerunning the same command picks up where it stopped.

## Where Rex fits

Rex runs the agents and keeps them alive on kant while the laptop is closed. `ferry` uses Rex on kant to launch the resumed agent in a named session, read `program_status`, and send `/exit` to an idle agent. Without Rex, `ferry` prints the resume command. Rex doesn't move bytes. Worth asking Rex for: a session ID in the program status escape sequence, and a host-to-host `rex cp` that would remove the SSH prerequisite.

## Build versus reuse

Build, on git and SSH. Read the prior art for edge cases.

| Tool | Verdict | Why |
|---|---|---|
| git | Use | Delta transport, integrity and history over SSH. |
| cct | Read | Covers both agents, but was last tested against older versions than kant runs, and only rewrites cwd. |
| entangle | Read | Best re-pathing rules. Live transfer goes through public wormhole servers. Claude only. |
| agent-sync | Read | Good writer check. Doesn't rewrite cwd. |
| mutagen, Syncthing, gut | No | Continuous two-way sync is the opposite of one owner. |
| tailscale file, magic-wormhole | No | Taildrop is alpha and needs root on Linux. Wormhole uses a public rendezvous server. |
| Polygraph | No | A commercial team product for cross-repo context. It doesn't move a tree or transcript between two machines. |

## What the prototypes showed

All of these ran on kant, with a fake home standing in for the other machine.

- `test/claude-resume.sh`: a 2.9 MB transcript with a subagent resumed from a different home with full context and the new cwd. 77 lines kept the old home inside free text, which is what the resume note is for.
- `test/codex-resume.sh` and `test/codex-roundtrip.sh`: Codex resumed from nothing but the rollout file, rebuilt `state_5.sqlite` and `thread_history_1.sqlite`, and recalled turns added on the other side after a round trip.
- `test/liveness.sh`: 11 checks on the Claude registry, the Codex thread lock under `--no-daemon`, Rex program status, and `/exit`. Rerun it after agent upgrades.
- `test/git-snapshot.sh`: 17 checks on a dirty worktree (partial staging, renames, symlinks, exec bits, deletions), a round trip, and a repo kant doesn't have.

Still unproven: a real macOS to Linux move with possibly different agent versions, rewind across machines, and the SSH transport.

## v1 scope

In: `to`, `from`, `ls`, `setup`. Claude Code and Codex. Git worktrees and checkouts under `~/src`. Creating a missing repo on kant. `.ferry/include` and `.ferry/state`. Approved `dehydrate` and `restore`. The ownership record and fencing. Trust carry-over. Rex launch and stop, with a printed fallback.

Left out on purpose:

- Directories that aren't git repos.
- Moving an agent mid-turn.
- Continuous or automatic sync, including handing off when the lid closes.
- Merging diverged trees or transcripts.
- Memory files, MCP config, credentials.
- Submodules and git-lfs.
- A third machine. The record has an owner field, so it's an extension, not a redesign.
- Background checkpoints to kant while the laptop owns a task. These would make a lost laptop recoverable, and they're the first thing I'd add.
