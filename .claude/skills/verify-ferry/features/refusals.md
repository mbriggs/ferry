# Refusals and interrupted handoffs

ferry refuses a handoff it can't make safely, exits 1, and prints one `ferry: ...` line saying what to do. Work on kant that the handoff would overwrite is offered a save to a branch instead, at a terminal. Some failures come after the task is claimed, so the record stays `handing-off` or `landing` until the user reruns the same direction, which picks up where the last run stopped. `--force` and `--force-version` override the owner and agent-version checks. The live-agent and wrong-owner refusals are in hand-off.md (`to-refuse-live`, `to-refuse-owner`).

## Sub-features

- `refuse-outside-src` refuses a repo that isn't under `~/src`.
- `refuse-older-agent` refuses when kant's agent is older than the laptop's: `kant has claude 1.9.0, older than 2.0.0 here; upgrade it or pass --force-version`. Nothing is claimed.
- `force-version` sends anyway and prints `versions claude 2.0.0 on laptop, 1.9.0 on kant`.
- `force` sends a task whose record names kant as owner.
- `refuse-loaded-there` refuses when kant already has the session open: `kant already has this session loaded (claude pid N is idle)`.
- `refuse-different-repo` refuses when kant's `~/src/demo` shares no root commit with the laptop's: `kant:src/demo is a different repository (no shared root commit); move it aside first`. Nothing is claimed.
- `refuse-unsent` refuses, with no terminal, when kant's copy has work the handoff would overwrite. It lists what changed there since kant last sent the task (`M app.txt`, `A stray.txt`), then `kant has work this handoff would overwrite; run ferry at a terminal to save it ...`. Nothing is claimed, and kant's files stay.
- `save-unsent` at a terminal offers `save it to branch ferry-saved/demo-<time> on kant, then hand off` or `stop; change nothing`. Saving prints `saved kant: ferry-saved/demo-<time>`, and the handoff overwrites kant's copy.
- `check` (`--check`) runs while the agent still runs, prints the same `receiver` lines plus `ferry to kant will offer to save it to a branch on kant first`, ends with `check nothing found that would stop the handoff`, and changes neither the record nor kant's files.
- `refuse-unfinished` refuses the other direction while a handoff is `handing-off`: `a handoff laptop -> kant is unfinished; finish it from that direction, or pass --force`.
- `resume-interrupted` reruns the same direction and prints `owner continuing an interrupted handoff (generation N)`.
- `refuse-landing` refuses the other direction while a handoff is `landing` (the code moved, restore didn't finish): `the handoff laptop -> kant hasn't finished; rerun it first`.
- `finish-landing` reruns the same direction and prints `owner kant owns demo (generation N); finishing the handoff`, then runs only the last steps.

## How to get to it (user POV)

- Any `ferry to kant` or `ferry from kant NAME` that hits one of these states. The fix is in the message: exit the agent, move a folder aside, upgrade, or rerun the direction that was interrupted.

## Driving it with world.sh

Preconditions:

- A fresh world for each group below. Use literal paths or `source $R/env` as SKILL.md says.

- **Older agent (`refuse-older-agent`, `force-version`).** Run `echo 2.0.0 > $LAPTOP/.stub-version; echo 1.9.0 > $KANT/.stub-version`, then `$W ferry $R to kant --yes --no-launch`. Exit 1 with the older-agent line, and kant has no `~/src/demo`. Run it again with `--force-version`. Exit 0 with the `versions` line above and `commit kant owns demo`. Remove both `.stub-version` files.
- **Force (`force`).** With kant owning the task, run `$W ferry $R to kant --yes --no-launch --force`. Exit 0, `commit kant owns demo (generation N)`, and `ls kant` shows the generation went up.
- **Loaded there (`refuse-loaded-there`).** In one shell call, fake a Claude on kant: `sleep 300 & pid=$!; mkdir -p $KANT/.claude/sessions; echo "{\"pid\":$pid,\"sessionId\":\"$SID\",\"status\":\"idle\"}" > $KANT/.claude/sessions/$pid.json`, run `$W ferry $R to kant --yes --no-launch --force`, then `kill $pid; rm $KANT/.claude/sessions/$pid.json`. Exit 1 with the loaded-there line. The record is unchanged.
- **Different repository (`refuse-different-repo`).** In a fresh world, run `mkdir -p $KANT/src/demo && git -C $KANT/src/demo init -q && git -C $KANT/src/demo -c user.name=k -c user.email=k@k commit -q --allow-empty -m other`, then `$W ferry $R to kant --yes --no-launch`. Exit 1 with the different-repository line. `$W ferry $R ls kant` shows `no tasks`. Then `mv $KANT/src/demo $KANT/src/demo-other` for the next group.
- **Unfinished (`refuse-unfinished`, `resume-interrupted`).** In a `--hooks` world (the one above, if you made it with `--hooks`), make dehydrate fail: `printf '#!/bin/sh\necho "db busy" >&2\nexit 1\n' > $WT/.ferry/dehydrate`. Run `$W tty $R to kant --yes --no-launch`, `$W wait $R 'run this dehydrate'`, `$W press $R Left Enter`, `$W wait $R '[exit '`. The pane shows `dehydrate failed on laptop`, and `ls kant` shows `demo laptop handing-off 1`. Run `$W ferry $R -C /tmp from kant demo --yes --no-launch`. Exit 1 with the unfinished line. Then `printf '#!/bin/sh\necho dumped\n' > $WT/.ferry/dehydrate` and `$W ferry $R to kant --yes --no-launch`. Exit 0 with `owner continuing an interrupted handoff (generation 1)`, `repo kant has no src/demo; creating it`, and `commit kant owns demo`.
- **Unsent work on kant (`check`, `refuse-unsent`, `save-unsent`).** In a fresh world, run `$W ferry $R to kant --yes --no-launch` and `$W ferry $R -C /tmp from kant demo --yes --no-launch`, then edit kant's copy: `echo edited > $KWT/stray.txt; echo more >> $KWT/app.txt`. In one shell call, fake a live Claude on the laptop as in hand-off.md's `to-refuse-live`, run `$W ferry $R to kant --session $SID --check`, then kill it and remove its file. Exit 0 with the `check` lines. Run `$W ferry $R to kant --yes --no-launch`. Exit 1 with the `refuse-unsent` lines, and `ls kant` still shows `laptop owned`. Run `$W tty $R to kant --yes --no-launch`, `$W wait $R 'would be overwritten'`, `$W screen $R save-prompt`, `$W keys $R`, `$W wait $R '[exit '`. Exit 0 with the `saved` line and `commit kant owns demo`. In `$KWT`, `stray.txt` is gone, `git branch --list 'ferry-saved/*'` shows one branch, and `git show <branch>:stray.txt` prints `edited`.
- **Outside ~/src (`refuse-outside-src`).** Run `mkdir -p $LAPTOP/elsewhere && git -C $LAPTOP/elsewhere init -q`, then `$W ferry $R -C $LAPTOP/elsewhere to kant --yes --no-launch`. Exit 1 with `... /laptop/elsewhere is outside ~/src; ferry only moves work under ~/src`.
- **Landing (`refuse-landing`, `finish-landing`).** In a fresh world, add a failing restore: `mkdir -p $WT/.ferry; printf '#!/bin/sh\necho "db missing" >&2\nexit 1\n' > $WT/.ferry/restore; chmod +x $WT/.ferry/restore`. Run `$W tty $R to kant --yes --no-launch`, `$W wait $R 'run this restore'`, `$W press $R Left Enter`, `$W wait $R '[exit '`. The pane shows `db missing`, `restore failed: restore failed on kant`, ``next fix it, then run `ferry to kant` again to finish``, and `[exit 1]`. `ls kant` shows `demo kant landing`. Then `$W ferry $R -C /tmp from kant demo --yes --no-launch` exits 1 with the landing line. Fix the hook on kant with `printf '#!/bin/sh\necho db loaded\n' > $KWT/.ferry/restore`, run `$W tty $R to kant --yes --no-launch`, approve the changed restore with `press Left Enter`, and wait for `[exit 0]`. The pane shows the finishing line and `restore kant: db loaded`, and `ls kant` shows `owned`.

## Gotchas

- The stubs on both sides are the same file, so `.stub-version` in each home is the only way to make the versions differ. Without it both report `0.0.0`.
- A failure that comes after the claim (a failing dehydrate) leaves `handing-off` in the record. A later `ferry from` refuses until the same direction is rerun or `--force` is passed.
- After the landing fix, the restore's sha has changed, so kant asks again.
