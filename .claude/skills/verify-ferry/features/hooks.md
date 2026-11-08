# Dehydrate and restore hooks

A repo can commit `.ferry/dehydrate` (runs on the sending machine before the code moves, typically dumping a database into `.ferry/state/`) and `.ferry/restore` (runs on the receiving machine after it lands). ferry shows each hook's contents and asks before running a hook whose exact contents that machine hasn't approved. A restore hook's summary line goes into the resume note.

## Sub-features

- `hook-approve` shows an unapproved hook with its sha and asks `run this <hook> on <machine>, now and whenever it's unchanged?`. On a terminal that's a Yes/No form with ` (←/→ then Enter)` after the question: Yes takes an arrow key and then Enter, and `y` or any other typed key submits No. With no terminal it answers no. A yes appends the sha to that machine's `~/.config/ferry/approved`.
- `hook-skip` prints `<hook> skipped` on no, and the handoff continues.
- `hook-state` sends files the dehydrate hook writes under `.ferry/state/` as extras.
- `hook-summary` puts the restore output's last line into the `resume` note as `Environment: ...`.
- `hook-every-run` asks before every run of a laptop hook that may run code kant wrote: the restore on every `ferry from`, and the dehydrate on every `ferry to` once the task has been to kant. The prompt reads `may run code from the other machine; ferry asks before every run here`.

## How to get to it (user POV)

- Commit `.ferry/dehydrate` and `.ferry/restore` in the repo, then `ferry to kant` or `ferry from kant NAME` from a terminal, and answer the prompts.

## Driving it with world.sh

Preconditions:

- Fresh world from `$W new --hooks`, doctor all `ok`.

- **Start.** Run `$W tty $R to kant --yes --no-launch`, `$W wait $R 'run this dehydrate'`, then `$W screen $R dehydrate-prompt`. The pane shows the dehydrate script, `isn't approved on laptop (sha ...)`, and the question above `Yes     No` buttons.
- **Approve dehydrate (`hook-approve`).** Run `$W press $R Left Enter`, `$W wait $R 'run this restore'`, then `$W screen $R restore-prompt`. The pane shows `extras .env (14 B), .ferry/state/db.dump (17 B)` and the restore prompt `isn't approved on kant`.
- **Approve restore (`hook-state`, `hook-summary`).** Run `$W press $R Left Enter`, `$W wait $R '[exit '`, then `$W screen $R done`. The pane shows `restore    kant: db loaded: dump from laptop`, a `resume` line containing `Environment: db loaded: dump from laptop`, and `[exit 0]`. `cat $KWT/.restored` prints `restored-to`. `$LAPTOP/.config/ferry/approved` and `$KANT/.config/ferry/approved` each hold one 64-hex sha.
- **Skip (`hook-skip`).** In a second `--hooks` world, run `$W ferry $R to kant --yes --no-launch` (no tty, so prompts answer no). Output has `dehydrate skipped` and `restore skipped`, exit 0, and kant has no `.restored`.
- **Every run (`hook-every-run`).** In the first world, run `$W tty $R -C /tmp from kant demo --yes --no-launch`. Approval is per machine, and kant approved only the restore, so `$W wait $R 'isn'"'"'t approved on kant'` first: kant's dehydrate prompts. Run `$W press $R Left Enter`, then `$W wait $R 'ferry asks before every run here'` and `$W screen $R every-run`. The laptop's restore prompt asks `run this restore on laptop now?`, with no "whenever it's unchanged", and lists what changed under `changed since this machine last sent the task:` (`no files changed` in this journey). Run `$W press $R Left Enter` and `$W wait $R '[exit '`; the pane shows `restore    laptop: db loaded: dump from kant`. The prompt comes back on every later `from`, even though the yes also adds the sha to `$LAPTOP/.config/ferry/approved`.

## Gotchas

- After a round trip, the next `ferry to` stops at the laptop's dehydrate prompt even though that sha is approved. A step that expects `ferry to` to finish unattended in a used `--hooks` world will hang until `wait` times out.
- Step labels are padded to 10 columns (`restore    kant: ...`). A `wait` for a label and its text with one space between them never matches.
- `$W keys $R y` on a hook prompt answers No: the pane prints `restore skipped` and the handoff goes on.
- Approval is per machine and per exact content. Editing a hook on kant changes its sha, so the next run prompts again.
