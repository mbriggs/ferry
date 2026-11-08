# ferry: hand a coding task and its agent session from the laptop to kant

## The short version

You start most work on the laptop: you poke at it with local resources, work out the details with the agent, and then hand it to kant so it can keep going after you close the lid. `ferry to kant` does that handoff. `ferry from kant` brings a task back when you want it local again.

`ferry` is a small Go binary installed on both machines. A task is one git worktree under `~/src` plus one Claude Code or Codex session. `ferry` moves the code as git objects and the transcript as a JSONL file with its home-directory paths rewritten, then resumes the full session on the other side. It keeps one owner per task in a record on kant. Both machines keep a resumable copy of the session; the newest one is wherever the task last moved. Environment state like databases, dev servers, and installed dependencies moves through two per-project scripts, `dehydrate` and `restore`, which only run after you approve them. Rex starts, watches, and stops the agents. SSH over the tailnet carries the bytes.

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
ferry from HOST [NAME] [--yes] [--no-launch]                                bring a task back here
ferry ls HOST                                                               tasks, owner, generation
ferry setup HOST [USER@HOST]                                                connect to HOST: hosts file, ssh, install ferry there
ferry doctor [HOST...]                                                      check this machine and each host, with fixes
ferry setup --claude-rules                                                  add the Claude Code permission rules ferry's own tests need
ferry push [HOST...] [--dry-run]                                            put this build on hosts that run a different one
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
apply      kant: src/app-wt/fix-auth matches the snapshot
session    7c1e9a02 staged on kant, 2 files, 1.9 MB
commit     kant owns fix-auth (generation 2)
session    kant: 7c1e9a02 is live
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
resume     cd ~/src/app-wt/fix-auth && claude --resume 7c1e9a02
```

Run from a terminal, `ferry from` then turns into the resumed agent, in the worktree, in the same terminal (or Rex pane). With `--no-launch`, or when its output isn't a terminal, it prints the command instead. Claude runs project hooks as it starts (SessionStart runs on resume), and `.claude/` travels, so kant could change what runs on the laptop the moment Claude starts. Before a pull touches anything, `ferry` fingerprints what Claude would load on the laptop: everything under the project's `.claude/` and each file a hook command in `settings*.json` names. If the handoff changed any of it, `ferry` lists the files and starts Claude only with a yes on that run; otherwise it prints the resume command. A rerun after an interruption can't know what was there before, so it asks whenever there's any such config.
If the agent on kant is mid-turn, `ferry from` refuses and tells you to wait.

A task doesn't have to have started on the laptop. Any git worktree under `~/src` is a task, and a worktree with no record lives on whichever machine has it; the first handoff in either direction writes the record. `ferry from kant` with no name lists what can be pulled: the agents running on kant, from Claude's session registry and Codex's thread locks, then tasks kant owns with no agent running. Each agent is placed in the worktree it is working in now, which is the last `cwd` in its transcript, so a Claude opened in `~/src` that went on to work in `~/src/app` counts as `src/app`. Picking one brings that worktree and that session. NAME narrows the list: a task name, a Rex session label, the start of a session id, or a path like `src/app`, which also reaches a worktree with no agent and no record.

A session that started in a folder above the worktree moves with it and resumes from that folder on the other side (`cd ~/src && claude --resume ...`), as long as it is now working inside the worktree. To stop an agent that ferry didn't launch, ferry reads `REX_SESSION` and `REX_BLOCK` from the agent's environment (for Codex, the process holding its thread lock), which Rex sets for everything it runs, and sends `/exit` to exactly that pane, only if Rex reports that kind of agent in it. If the pane can't be found that way, ferry asks you to exit the agent; it never guesses by session name for an agent it didn't launch. Every check that can refuse a handoff runs before the agent is stopped. On the way back, `ferry to` considers the session the task last moved with alongside the worktree's newest, so a session that started above the worktree returns with it. Placing a session refuses if an agent has that session loaded on the receiver.

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

Every ignored file travels too, so a worktree arrives the way it was left: `.env` files, generated output, and `.claude/`, the agent's own project folder (skills, commands, local settings), which is often ignored globally. Three things stay. Dependency and build trees (`node_modules`, `_build`, `deps`, `target`, `.venv`, `vendor`, `dist`, `build` and caches like them) are built for one OS, and rebuilding them is `restore`'s job. Those names are guesses, so they don't apply inside `.claude/` or `.ferry/`, where a skill may well be called `build`. Claude's own git worktrees under `.claude/worktrees/` stay. And `.ferry/exclude` lists anything else, one pattern per line: a glob against the base name or the whole path, or a directory when it ends in `/`. `.ferry/state/` always travels. What stays, at any depth, is listed in the output (`left  staying on kant (dependencies, build output, .ferry/exclude): node_modules/, dist/`). These files go as a tar stream over SSH, never through git. Every tar stream ends with a sha256 for each file. The receiver hashes what it writes, syncs it to disk, and refuses the transfer if anything is missing, different, or cut short, before anything is installed. Large files, like multi-gigabyte database dumps in `.ferry/state`, get three more things. An extra the receiver already has with the same size and sha256 isn't sent at all. A transfer cut short leaves a partial file named after the content's sha, and the next run picks up where it stopped. Files move into the worktree by rename, not by copying. Each machine caches the sha256 of the extras it has hashed or installed, keyed by size and mtime, so an unchanged dump isn't read again on the next handoff. Partial files only become real files after the whole stream checks out. `ferry` lists each file and its size, folded into one line per top-level folder when there are many, and asks before sending (`--yes` skips the prompt), and writes them with mode 0600 on the other side. Deletions carry over too. The record keeps the path and hash of each extra a machine sent, and when the task comes back, an extra the other side deleted is removed, as long as it's still exactly what was sent. A removed file moves to `~/.local/state/ferry/parked/` rather than being deleted, and a file that changed in the meantime is kept.

Git hooks in `.git/hooks` travel too, since git never transfers them; git's own `*.sample` files stay. A hook that's new or different on the sender is installed on kant as it is. Coming to the laptop, a hook runs on your next `git commit` there, so `ferry` lists them and installs them only with a yes on that run, which `--yes` doesn't give. A replaced hook is parked. Claude's project memory (`~/.claude/projects/<folder>/memory/`) goes with the session, rewritten into the receiver's project folder and merged with what's there: an incoming file replaces one that differs, which is parked, and files only the receiver has stay.

## Project scripts: dehydrate and restore

Each project can have two optional executables:

- `.ferry/dehydrate` runs on the machine giving up the task, after the agent stops and before the snapshot. It writes state that git doesn't hold into `.ferry/state/` and stops anything holding the working tree.
- `.ferry/restore` runs on the machine taking the task, after ownership moves and before the agent starts. It installs dependencies, loads what `dehydrate` saved, and starts services. On kant it can start long-running services in their own panes with the local `rex run`.

Scripts never run without approval, and trust runs one way. kant is where agents work unattended, so anything arriving from kant is treated as less trusted than anything arriving from the laptop.

- On the machine that runs a hook, `ferry` keeps approved hashes in `~/.config/ferry/approved`. A hook whose hash isn't approved is shown in full on the laptop, and `ferry` asks before running it. Hooks over 64 KB can't be shown in full, so they never run. The display escapes control characters, so a script can't hide lines with carriage returns or terminal escapes. `ferry` runs a private copy of the exact bytes it hashed.
- A hook on the laptop that may run code kant wrote asks every time, even if it's approved. That's `restore` during `ferry from`, and `dehydrate` during `ferry to` once the task has been on kant. Approval by hash only covers the hook file. Hooks run the repo's build tooling (`npm install`, `bin/setup`, `docker compose`), and an agent on kant can change what that tooling runs without touching the hook. So before it runs, `ferry` shows what changed since the laptop last sent the task, and calls out build and setup files like `package.json`, lockfiles, `Makefile` and `bin/`. The laptop works that list out from its own `refs/ferry-sent/<task>`, never from anything kant stores.
- `--yes` never counts as approval. If you decline, or there's no terminal to ask on, the handoff continues without that step, and the resume note says the environment is cold.

This is the guarantee: nothing from kant runs on the laptop without you saying yes on that run. ferry doesn't sandbox anything. Once you say yes, kant's changes run with your permissions, just as they would if you pulled the branch and ran `npm install` yourself.

`ferry` passes both scripts `FERRY_DIRECTION` (`to` or `from`), `FERRY_TASK`, `FERRY_ROOT` and `FERRY_STATE`. The contract has three rules:

- **Rerunnable.** Rerunning a handoff may run either script again, so running it twice has to end in the same state as running it once.
- **Exit status is the verdict.** If `dehydrate` fails, the handoff stops before anything moves. If `restore` fails, the task still belongs to the receiver, because the code and transcript arrived intact, but the agent doesn't start.
- **A one-line summary on the last line of stdout.** `ferry` prints it and adds it to the resume note.

### The ferry-scripts skill

A `dehydrate` that dumps a database should compress the dump with `zstd -T0`, which uses every core. A plain SQL dump shrinks 5 to 10x, and that does more for a slow link than anything in the transport. `ferry` ends every handoff with how long each step took, leaving out time spent at prompts, so it's clear whether the dump, the transfer, or `restore` is the slow part. A skill writes and maintains these scripts. It reads the project's lockfiles, `mise.toml`, compose files, `Procfile`, `.env.example` and README setup steps, and writes short idempotent scripts. A script only counts as working after a real round trip on a scratch branch. When `restore` fails, `ferry` suggests running the skill, which compares the project against what the scripts assume and updates them. It comes after the binary, once two or three projects have scripts.

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

**Trust.** A resumed agent in a folder the receiver has never trusted stops at the trust prompt, and Claude's default answer there is "No, exit". `ferry` carries trust over only if you trusted the folder on the sending machine, and it does it the way you would: after launching in Rex, it answers the prompt in the pane (Down then Enter for Claude, Enter for Codex). If neither machine trusts the folder, `ferry` doesn't launch, and prints the resume command instead. An agent at the trust prompt hasn't registered, so the liveness checks can't see it: a `ferry from` would copy the transcript out from under it, and a rerun would start a second one. For the same reason, `ferry` waits until the launched agent registers, and kills its Rex session if that takes over a minute. Codex ignores trust passed with `-c`, and editing `~/.claude.json` or the stowed `config.toml` behind a running agent's back is riskier than answering the question it asks.

**Versions.** If the receiver's agent is older than the sender's, `ferry` refuses unless you pass `--force-version`.

## One owner at a time

kant holds one record per task at `~/.local/state/ferry/tasks/<task>.json`: `{owner, generation, state, agent, session, fingerprint}`. Every handoff goes through kant, so the record is reachable whenever a handoff can happen. Each write is a compare-and-swap on `generation` under `flock`, and every transition bumps the generation. A handoff also holds a `flock` on the task on the laptop, which drives every handoff, so a second `ferry` on the same task is refused instead of interleaving with the first. The kernel releases it when `ferry` exits, however it exits. A call that `ferry` made on kant can outlive it, so on kant each `ferry remote` call holds a lock on the task too, and a rerun's calls wait for a dead run's call to finish.

Nothing live moves before the commit point: the session waits on the receiver in a staging directory, already checked and rewritten. A failure before the commit leaves both machines as they were. After the commit, the staged copy moves into place; if the receiver already had a copy of that session, from an earlier trip, the old one goes to `~/.local/state/ferry/parked/` first, so turns you added there by hand aren't lost. The sending machine keeps its copy and can still resume it. That's deliberate: the point is to move everything over, not to guard against resuming the old one. Resuming both does fork the conversation, and since every Claude resume re-links the session to its Remote Control cloud session, the two copies would take that link from each other.

The record never expires on its own. A closed laptop is the normal case, and automatic takeover is how two machines end up running the same session.

### Steps

1. Preflight: task, agent stopped, versions. Record `handing-off` at generation g+1, or continue one this direction already started.
2. Run `dehydrate` on the source, if approved.
3. Create the repo on the receiver if missing. Snapshot, then push or fetch the refs.
4. Apply and verify the tree, then send extras.
5. Send the session files to a staging directory on the receiver, check them, and rewrite their paths.
6. Record the receiver as owner. This is the commit point.
7. Move the staged session into place, run `restore` on the receiver if approved, and launch the agent: in a new Rex session on kant for `to`, in the current terminal for `from`.

A failure before step 6 leaves the source as owner, untouched. A failure after step 6 leaves the receiver as owner. Either way, rerunning the same command picks up where it stopped.

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

In: `to`, `from`, `ls`, `setup`. Claude Code and Codex. Git worktrees and checkouts under `~/src`. Creating a missing repo on kant. Ignored files, `.ferry/exclude` and `.ferry/state`. Git hooks. Claude project memory. Approved `dehydrate` and `restore`. The ownership record and fencing. Trust carry-over. Rex launch and stop, with a printed fallback.

Left out on purpose:

- Directories that aren't git repos.
- Moving an agent mid-turn.
- Continuous or automatic sync, including handing off when the lid closes.
- Merging diverged trees or transcripts.
- Memory files, MCP config, credentials.
- Submodules and git-lfs.
- A third machine. The record has an owner field, so it's an extension, not a redesign.
- Background checkpoints to kant while the laptop owns a task. These would make a lost laptop recoverable, and they're the first thing I'd add.
