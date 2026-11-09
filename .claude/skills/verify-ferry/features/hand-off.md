# Hand a task to kant

In a repo under `~/src`, after exiting the agent, the user runs `ferry to kant`. The repo's commits, staged, unstaged, and untracked changes, its ignored files (except dependency and build dirs), and the newest Claude or Codex transcript for that directory move to the same path under kant's home. The transcript's paths are rewritten to kant's home, the laptop keeps its own copy, and kant becomes the task's owner.

## Sub-features

- `to-code` recreates the repo on kant (or reuses it if it shares history) with the same commits, index, and working tree.
- `to-extras` sends every ignored file and writes each one 0600 whatever its mode on the laptop. It leaves behind dependency and build dirs (`node_modules` and the rest of `heavyDirs` in `files.go`, except inside `.claude/` and `.ferry/`, where only `.git` stays), `.claude/worktrees/`, `.DS_Store`, and paths matching `.ferry/exclude` (one glob per line against the base name or the whole path, a trailing `/` for a directory, `#` comments). `.ferry/state/` always travels, even when it isn't ignored. What stays, including build dirs nested in an ignored dir (`cache/dist/`), is named on a `left  staying on laptop (dependencies, build output, .ferry/exclude): ...` line; `.claude/worktrees/` stays without a mention.
- `to-extras-declined` sends no extras when the prompt is declined, and the handoff still finishes.
- `to-session` installs the transcript on kant with `cwd` rewritten. The laptop keeps its copy.
- `to-record` writes a task record on kant: owner `kant`, state `owned`.
- `to-resume` prints the `claude --resume <id> '[ferry] ...'` line when `--no-launch` is set.
- `to-refuse-live` refuses while the agent is still running on the laptop.
- `to-refuse-owner` refuses a second `ferry to kant` once kant owns the task.

## How to get to it (user POV)

- `cd ~/src/<repo> && ferry to kant`, after `/exit` in the agent.
- Flags: `--yes` sends listed ignored files without asking, `--no-launch` prints the resume command, `--agent claude|codex`, `--session ID`, `--force`, `--force-version`.

## Driving it with world.sh

Preconditions:

- Fresh world from `$W new`, doctor all `ok`.

- **Before.** Run `$W state $R before`. Laptop shows `MM app.txt`, `D  gone.txt`, `?? untracked.txt`, ahead 1, ignored `.env` and `node_modules/x/index.js`. Kant shows `(no ~/src/demo)`.
- **Refuse a live agent (`to-refuse-live`).** Do this whole step in one shell call, since `$pid` won't survive to the next. Fake a running Claude: `sleep 300 & pid=$!; mkdir -p $LAPTOP/.claude/sessions; echo "{\"pid\":$pid,\"sessionId\":\"$SID\",\"status\":\"idle\"}" > $LAPTOP/.claude/sessions/$pid.json` (after `source $R/env`). Run `$W ferry $R to kant --yes --no-launch`. Exit 1 and `claude pid N is idle; exit the agent, then run this again`. Then `kill $pid; rm $LAPTOP/.claude/sessions/$pid.json`. Kant still has no `~/src/demo` and `$W ferry $R ls kant` prints `no tasks`.
- **Hand off.** Run `$W ferry $R to kant --yes --no-launch`. Exit 0, with steps `versions claude 0.0.0 on laptop, 0.0.0 on kant`, `owner new task`, `repo kant has no src/demo; creating it`, `left staying on laptop (dependencies, build output, .ferry/exclude): node_modules/`, `extras .env (14 B)`, `session 11111111 staged on kant, paths ... -> ...`, `commit kant owns demo (generation N)`, `session kant: 11111111 is live`, a `resume cd ~/src/demo && claude --resume 11111111-...` line, and a closing `time` line.
- **After (`to-code`, `to-extras`, `to-session`, `to-record`).** Run `$W state $R after-to`. Kant's status and staged diff match the laptop's before-state, including `## main...origin/main [ahead 1]` (remotes.md covers that); the log has `unpushed` on top, `.env mode 600`, no `node_modules`. Kant has the transcript under `-tmp-ferry-verify-...-kant-src-demo/` and `grep cwd` on it shows only kant paths. The laptop still has its transcript under `.claude/projects`, and nothing under `.local/state/ferry/parked/`. The record has `"owner":"kant"` and `"state":"owned"` (match the fields; the JSON has other keys around them).
- **Every ignored file (`to-extras`).** In a second fresh world, run `echo '{}' > $WT/secrets.json; printf 'secrets.json\n.claude/\n' >> $WT/.gitignore; mkdir -p $WT/.claude/worktrees/x; echo '{}' > $WT/.claude/settings.local.json; echo wt > $WT/.claude/worktrees/x/f`, then `$W ferry $R to kant --yes --no-launch`. The `extras` line is `.claude/settings.local.json (3 B), .env (14 B), secrets.json (3 B)`, the `left` line names only `node_modules/`, kant has `secrets.json` at mode 600, and `find $KWT/.claude -type f` shows only `settings.local.json`, not `worktrees/x/f`.
- **Exclude (`to-extras`).** In a third fresh world, run `mkdir -p $WT/.ferry; printf 'secrets.json\n' > $WT/.ferry/exclude; echo '{}' > $WT/secrets.json; echo secrets.json >> $WT/.gitignore`, then `$W ferry $R to kant --yes --no-launch`. The `extras` line is `.env (14 B)`, the `left` line is `node_modules/, secrets.json`, and kant has `.env` but no `secrets.json`.
- **Decline extras (`to-extras-declined`).** In a fourth fresh world, run `$W ferry $R to kant --no-launch` (no `--yes`, and no tty, so the prompt answers no). Output has `extras .env (14 B)` then `extras skipped`, exit 0, and kant has no `.env`.
- **Refuse when kant owns it (`to-refuse-owner`).** Run `$W ferry $R to kant --yes --no-launch` again. Exit 1 and ``kant owns demo, not laptop; use `ferry from kant demo` ``.

## Gotchas

- A transcript modified in the last 2 seconds makes ferry wait up to 10 seconds for it to settle. Backdate edited transcripts with `$W backdate FILE`.
- `--yes` never approves hooks. In a `--hooks` world, `world.sh ferry` answers the hook prompts no and prints `dehydrate skipped`. Use hooks.md for that path.
- The world uses a plain repo. A linked worktree (`src/app-wt/fix-auth`) is covered by `TestRoundTrip` in `handoff_test.go`, not here.
