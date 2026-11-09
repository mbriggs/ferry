# Remotes travel with the code

Every handoff gives the receiver the sender's remotes (URLs and push URLs), the branch's upstream, and the sender's remote-tracking refs where they're newer. ferry never fetches or pushes to a remote. A repo ferry creates takes the config silently. In an existing repo ferry prints a `remotes` line listing each change and asks `change these remotes on <host>?`. `--yes` answers that going to kant, never coming to the laptop.

## Sub-features

- `remotes-fresh` gives a repo ferry creates on kant the laptop's remotes and upstream without asking: `remotes kant: add remote origin <url>; set main's upstream to origin main (was none)`, then `remotes kant: moved 1 remote-tracking ref forward`.
- `remotes-add` adds a remote the laptop gained since the last trip, answered by `--yes` going to kant.
- `remotes-ask` lists a URL kant changed (`set remote origin to <new> (was <old>)`) and asks on the laptop, even with `--yes`. Yes rewrites the laptop's URL.
- `remotes-declined` keeps the laptop's URL when the prompt answers no (no terminal), prints `remotes kept laptop's remotes as they are`, and leaves that remote's tracking refs alone. `TestRemotesTravel` in `remotes_test.go` also covers this and the fast-forward-only rule.
- `remotes-quiet` prints no `remotes` line when nothing differs.

## How to get to it (user POV)

- `ferry to kant` and `ferry from kant NAME`, as in hand-off.md and bring-back.md. The user changes remotes with ordinary git (`git remote add`, `git remote set-url`) on either machine between trips.

## Driving it with world.sh

Preconditions:

- Fresh world from `$W new`, doctor all `ok`. Call world.sh by its literal path if a hook refuses `$W` as a command name.

- **Fresh repo (`remotes-fresh`).** Run `$W ferry $R to kant --yes --no-launch`. The output has the two `remotes` lines above. `$W state $R after-to` shows `## main...origin/main [ahead 1]` on both sides, and `$KWT/.git/config` has `[remote "origin"]` and `[branch "main"]` with `remote = origin`.
- **Quiet (`remotes-quiet`).** Run `$W ferry $R -C /tmp from kant demo --yes --force-version`. Exit 0 and no `remotes` line.
- **Add (`remotes-add`).** Append `[remote "fork"]` with `url = https://example.com/fork.git` to `$WT/.git/config`, then `$W ferry $R to kant --yes --no-launch`. Output has `remotes kant: add remote fork https://example.com/fork.git` and no prompt; `$KWT/.git/config` has the fork section with git's default fetch refspec.
- **Declined (`remotes-declined`).** Change `origin`'s `url` in `$KWT/.git/config` to `https://example.com/elsewhere.git`, then `$W ferry $R -C /tmp from kant demo --yes --force-version`. Output has the `set remote origin ...` line, `remotes kept laptop's remotes as they are`, and `remotes origin's URLs differ on laptop; left its remote-tracking refs`; `$WT/.git/config` still has the world's `origin.git` URL. Hand it back to kant with `--yes`, which resets kant's `origin` to the laptop's URL, and change kant's URL again for the next step.
- **Ask (`remotes-ask`).** Run `$W tty $R -C /tmp from kant demo --yes --no-launch --force-version`, `$W wait $R 'change these remotes'`, `$W screen $R remotes-prompt`. The pane shows the `set remote origin` line and `change these remotes on laptop?`. Run `$W keys $R y` and `$W wait $R '[exit '`: `commit laptop owns demo`, `[exit 0]`, and `$WT/.git/config` has `url = https://example.com/elsewhere.git` under `origin`.

## Gotchas

- `ferry from` in a world needs `--force-version`: kant's side finds the real `claude` through the login shell's PATH and reports its version, which is newer than the laptop's 0.0.0 stub.
- A worktree-isolated session's hook refuses `git -C` aimed at the world, so read and edit `.git/config` directly and use `$W state` for status and logs.
- `git config --add` puts a replaced URL after the section's fetch line. The section still has one `url`.
