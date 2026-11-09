# List tasks on a host

`ferry ls kant` prints the task records kept on kant: name, owner, state, generation, agent, and path. A user runs it to find the name for `ferry from`, and as the README's connection check, where a fresh setup prints `no tasks`.

## Sub-features

- `ls-empty` prints `no tasks` when kant has no records.
- `ls-table` prints one row per task with the current owner and state, newest first.
- `ls-self` on the hub itself (`ferry ls kant` run on kant) reads its own records with no remote call.
- `ls-usage` with no host prints the usage text and exits 2.

## How to get to it (user POV)

- `ferry ls kant` from any directory, on the laptop or on kant.

## Driving it with world.sh

Preconditions:

- A fresh world.

- **Empty (`ls-empty`).** Run `$W ferry $R ls kant`. Exit 0, output `no tasks`.
- **After a handoff (`ls-table`).** Run `$W ferry $R to kant --yes --no-launch`, then `$W ferry $R ls kant`. A header `NAME OWNER STATE GEN AGENT PATH` (padded columns, so match `grep -E '^NAME +OWNER'`) and a `demo` row with owner `kant`, state `owned`, agent `claude`, path `~/src/demo`.
- **After bringing it back.** Run `$W ferry $R -C /tmp from kant demo --yes`, then `ls` again. The row's owner is `laptop`, state `owned`, and the generation is higher.
- **On the hub (`ls-self`).** Run `$W ferry $R --as kant ls kant`. Exit 0 and the same `demo` row.
- **No host (`ls-usage`).** Run `$W ferry $R ls`. Exit 2, and the output starts with `usage:`.

## Gotchas

- That table is the plain layout, which `world.sh ferry` always gets. On a terminal (`world.sh tty`) ferry prints a styled list instead: no GEN column, a `✓` or `!` per state, and a dim `gen N` at the end of each row.
- "Newest first" means most recently written, and every handoff rewrites the record, so a task just brought back moves to the top.
- The generation goes up by more than one per handoff (claim, attempt, commit, finish). Assert that it increased, not a specific value, unless you're testing the record code.
