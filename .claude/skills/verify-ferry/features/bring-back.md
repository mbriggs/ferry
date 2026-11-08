# Bring a task back

From anywhere on the laptop, the user runs `ferry from kant NAME`, or `ferry from kant` to pick from a list. ferry takes the work done on kant (new commits, uncommitted edits, ignored files, the longer transcript) back to the laptop path, rewrites the transcript's paths to the laptop's home, and makes the laptop the owner again. Kant keeps its transcript. In a terminal, ferry then replaces itself with `claude --resume` in that terminal.

## Sub-features

- `from-code` brings kant's commits and uncommitted changes back, keeping changes from the laptop that kant didn't touch.
- `from-session` returns the transcript with kant's new turns and laptop `cwd`s. Kant keeps its copy. The laptop's own older copy, left there by the hand-off, is parked under `~/.local/state/ferry/parked/<task>-replaced-<stamp>/`.
- `from-extras-delete` removes on the laptop an ignored file that was deleted on kant, parking the laptop's copy.
- `from-record` sets the owner back to `laptop`.
- `from-pick` with no NAME lists kant's tasks and running agents as a select list on the terminal, under `pull from kant:`. Without a terminal it prints the list and exits 1 instead.
- `from-exec` resumes the agent in the user's terminal: with a tty and no `--no-launch`, ferry prints `resume cd ~/src/demo && claude --resume <id>` without the note, then execs `claude`.

## How to get to it (user POV)

- `ferry from kant [NAME]`. NAME can be a task name from `ferry ls kant`, a path (`src/demo` or `~/src/demo`), a Rex session label, or a session-id prefix of 8 or more characters. The cwd doesn't matter.
- Flags: `--yes`, `--no-launch` (print the resume line instead of execing the agent here), `--force`, `--force-version`.

## Driving it with world.sh

Preconditions:

- A world after hand-off.md's **Hand off** step (kant owns `demo`).

- **Work on kant.** Run `$W kant-work $R`. Also delete the extra: `rm $KWT/.env`. Then `$W state $R before-from`.
- **Bring it back.** Run `$W ferry $R -C /tmp from kant demo --yes`. There's no terminal, so ferry prints the resume line rather than execing. Exit 0, with `repo laptop: src/demo exists, same history`, `extras deleted on kant since laptop sent them: .env` and `extras laptop: removed .env (parked in ~/.local/state/ferry/parked/)`, `commit laptop owns demo (generation N)`, and a `resume` line with the `[ferry] This session moved from kant to laptop...` note.
- **After (`from-code`, `from-session`, `from-extras-delete`, `from-record`).** Run `$W state $R after-from`. The laptop log has `work on kant` on top of `unpushed`, `app.txt` is still staged and has the `v4 edited on kant` line unstaged, `gone.txt` is still staged deleted, `untracked.txt` is there. `tail -1` of the laptop transcript has `The codeword is HERON` and a laptop `cwd`. The laptop has no `.env`. Its parked files are `src-demo-<hash>-extras-<stamp>/.env` and `src-demo-<hash>-replaced-<stamp>/.claude/projects/.../<sid>.jsonl`. Kant still has its transcript under `.claude/projects` and nothing parked. The record shows owner `laptop`.
- **Pick (`from-pick`).** In a fresh world after **Hand off**, run `$W ferry $R -C /tmp from kant --yes --no-launch`. Exit 1, with `ferry: pull from kant: more than one match, and no terminal to ask on:` and a `demo` row reading `~/src/demo` and `owned (no agent running)` (padded columns, so match the parts). Then run `$W tty $R -C /tmp from kant --yes --no-launch` and `$W wait $R 'pull from kant'`, then `$W screen $R picker`. The pane shows `pull from kant:` and the highlighted row `> demo  ~/src/demo  owned (no agent running)`. Run `$W keys $R` (Enter alone picks the highlighted row) and `$W wait $R '[exit '`. The pane shows `commit laptop owns demo` and `[exit 0]`.
- **Resume here (`from-exec`).** In that world, hand it back with `$W ferry $R to kant --yes --no-launch`, then run `$W tty $R -C /tmp from kant demo --yes` (no `--no-launch`) and `$W wait $R '[exit '`. The pane shows `resume cd ~/src/demo && claude --resume 11111111-2222-3333-4444-555555555555` with no note, then `world.sh: blocked claude --resume 11111111-... [ferry] This session moved from kant to laptop...` and `[exit 1]`. The stub's argv shows the note ferry passed to the agent. The task is already the laptop's by then.

## Gotchas

- `kant-work` commits only `kant-was-here.txt`. If you commit on kant with a bare `git commit`, the laptop's staged changes go into that commit and `from-code` can't show the index surviving.
- The record lives on kant (the hub) even when the laptop owns the task. `state` prints it under `===== kant`.
- The no-terminal pick message says "more than one match" even when the list has one row.
- The picker is a select list, not a number prompt. Typing a digit does nothing, and `$W keys $R` picks the highlighted row.
- A world can't reach a Rex-label NAME: the lookup reads `/proc` (Linux only) and asks a real Rex, which the stub blocks.
