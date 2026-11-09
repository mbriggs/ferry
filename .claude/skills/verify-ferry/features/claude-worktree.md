# Hand off a worktree Claude made

`claude --worktree NAME` puts the session in `~/src/<repo>/.claude/worktrees/NAME` on branch `worktree-NAME`. The user runs `ferry to kant` inside that worktree, or `ferry to kant NAME` from the main checkout. ferry treats it as a linked worktree: it lands at the same path on kant on the same branch. It finds the transcript even after Claude refiled it into the main checkout's project folder, and it makes the received copy a plain session, so an `/exit` there won't delete the worktree. `ferry from kant NAME` brings it back the same way.

## Sub-features

- `cwt-unlock` clears the git worktree lock a Claude that has since exited left behind, printing `worktree  unlocked ~/src/demo/.claude/worktrees/feat: the Claude that locked it has exited`.
- `cwt-to` sends the worktree. The `task` line reads `feat  (src/demo/.claude/worktrees/feat, repo src/demo)`. Kant gets the main checkout plus the worktree on `worktree-feat`, and the transcript under kant's project folder for the worktree path. The resume line is `cd ~/src/demo/.claude/worktrees/feat && claude --resume <id>`.
- `cwt-plain` appends `{"type":"worktree-state",...,"worktreeSession":null}` to the copy kant receives when the session was still in the worktree. The laptop's copy is unchanged.
- `cwt-refiled` finds a transcript that `/exit` and Keep moved into the main checkout's project folder (`relocated`, then a null worktree-state). On bring-back the laptop gets the session in the worktree's folder, and ferry parks the refiled copy under `~/.local/state/ferry/parked/<task>-replaced-<stamp>/`.
- `cwt-pick` in the main checkout picks a worktree: a `send which worktree?` select list on a terminal, with the checkout itself marked `(this checkout)`; `ferry to kant NAME` by name; `no worktree of ~/src/demo matches "NAME"` and exit 1 for an unknown NAME. With no terminal and no NAME it prints `task  sending this checkout; name a worktree to send that instead` and sends the checkout.
- `cwt-refuse-live` refuses while Claude is still running in its worktree: `claude pid N is idle in its own worktree; close its pane (or /exit and pick Keep; /exit deletes a worktree with no changes), then run this again`.

## How to get to it (user POV)

- Close the Claude pane, or `/exit` and pick Keep, then `cd ~/src/<repo>/.claude/worktrees/NAME && ferry to kant`.
- From the main checkout: `ferry to kant NAME`, or `ferry to kant` and pick from the list.
- `ferry from kant NAME` to bring it back. The `ferry` Claude skill (`skills/ferry/`) does the close-and-send for the user from inside the session, but it needs Rex and a real agent, so a world can't drive it.

## Driving it with world.sh

Preconditions:

- Fresh world from `$W new`, doctor all `ok`. `$R/env` sets `CWT` (the laptop worktree), `KCWT` (kant's), and `CWSID` (its session).

- **Closed pane (`cwt-unlock`, `cwt-to`, `cwt-plain`).** Run `$W claude-worktree $R closed`. `git -C $WT worktree list --porcelain` shows a `locked claude session feat (pid N ...)` line. Run `$W ferry $R -C $CWT to kant --yes --no-launch`. Exit 0, with the `worktree unlocked` line first, the `task` line above, `snapshot branch worktree-feat at ...`, `commit kant owns feat`, and the worktree resume line. The laptop's worktree list has no `locked` line. `cat $KCWT/feat.txt` prints `work in progress`, and `git -C $KCWT branch --show-current` prints `worktree-feat`. Kant's transcript (`find $KANT/.claude/projects -name "$CWSID.jsonl"`) has only `$KCWT` cwds and ends with the null worktree-state. The laptop's copy still ends with its `edited feat.txt` turn.
- **Bring it back.** Append a turn to kant's transcript (`{"type":"assistant","cwd":"$KCWT","sessionId":"$CWSID","message":"did kant work"}`), `$W backdate` it, then run `$W ferry $R -C /tmp from kant feat --yes --no-launch`. Exit 0, `commit laptop owns feat`, the same resume line. The laptop's transcript in the worktree's project folder ends with `did kant work`, with laptop cwds and a null worktree-state, and its old copy is parked under `src-demo--claude-worktrees-feat-<hash>-replaced-<stamp>/`.
- **Refiled, picked by name (`cwt-refiled`, `cwt-pick`).** In a second fresh world, run `$W claude-worktree $R kept`. Run `$W ferry $R to kant nope --yes --no-launch`: exit 1 with the `matches "nope"` line. Run `$W tty $R to kant --yes --no-launch` and `$W wait $R 'send which worktree'`, then `$W screen $R wt-picker`. The pane lists `demo ~/src/demo ... (this checkout)` and `feat ~/src/demo/.claude/worktrees/feat claude 22222222`. Run `$W press $R C-c` and `$W wait $R '[exit '`: `nothing picked`, `[exit 1]`. Run `$W ferry $R to kant feat --yes --no-launch`: exit 0, `task feat ...`. Append a turn on kant as above and run `$W ferry $R -C /tmp from kant feat --yes --no-launch`. The laptop's session is now in the worktree's project folder, the main checkout's folder no longer has `$CWSID.jsonl`, and the parked `-replaced-` folder holds it under the main checkout's project slug.
- **No terminal, no NAME (`cwt-pick`).** In a third fresh world, run `$W claude-worktree $R kept`, then `$W ferry $R to kant --yes --no-launch`. Output starts with the `sending this checkout` line, then `task demo (src/demo, repo src/demo)`, and the handoff of the main checkout finishes with exit 0.
- **Live in its worktree (`cwt-refuse-live`).** In a fourth fresh world, run `$W claude-worktree $R closed`, then in one shell call: `sleep 300 & pid=$!; mkdir -p $LAPTOP/.claude/sessions; echo "{\"pid\":$pid,\"sessionId\":\"$CWSID\",\"status\":\"idle\"}" > $LAPTOP/.claude/sessions/$pid.json; $W ferry $R -C $CWT to kant --yes --no-launch; kill $pid; rm $LAPTOP/.claude/sessions/$pid.json`. Exit 1 with the `worktree unlocked` line (the lock's pid is dead even though this Claude is live), then the in-its-own-worktree line. `$W ferry $R ls kant` prints `no tasks`.

## Gotchas

- `$W state` and `$W kant-work` look only at `src/demo`. Check the worktree with `git -C $CWT`, `git -C $KCWT`, and `find`, and append kant's turn by hand. A worktree-isolated session's hook refuses those `git -C` calls, so run this file's steps from the primary checkout with the lane's `world.sh` path.
- Use the helper's `$CWSID`, not `$SID`. ferry parks every other laptop copy of a session id when it brings one back, so reusing the demo session's id parks the main checkout's transcript too.
- The helper adds `.claude/worktrees/` to the laptop's `.git/info/exclude`, as a repo whose `.gitignore` covers it would. ferry doesn't copy `info/exclude`, so after a worktree has been to kant, kant's main checkout shows `?? .claude/`, and a later `ferry to kant` of the main checkout stops at `src/demo has changes that weren't handed off` (no terminal: `kant has work this handoff would overwrite`). That's why the no-terminal step uses its own world.
- Stopping a Claude still in its worktree on kant (SIGTERM instead of typing `/exit`) needs a real Rex. `TestPullStopsAClaudeWorktreeAgentWithSigterm` covers it.
