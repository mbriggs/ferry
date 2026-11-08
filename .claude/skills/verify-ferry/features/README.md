# ferry verification map

This directory is the maintained list of what to verify in ferry, from the user's side of the terminal. Read this index, then drive the feature file that matches your change. `../SKILL.md` has launch, doctor, evidence, and cleanup.

| File | Feature | Command |
|---|---|---|
| [hand-off.md](hand-off.md) | Hand a task to kant | `ferry to kant` |
| [bring-back.md](bring-back.md) | Bring a task back | `ferry from kant [NAME]` |
| [list.md](list.md) | List tasks on a host | `ferry ls kant` |
| [hooks.md](hooks.md) | Dehydrate and restore hooks with approval | `.ferry/dehydrate`, `.ferry/restore` |
| [setup.md](setup.md) | Add Claude permission rules | `ferry setup --claude-rules [--dry-run]` |
| [connect.md](connect.md) | Connect a host and check it | `ferry setup HOST [USER@HOST]`, `ferry doctor [HOST...]` |

Not mapped here because a world can't reach it: launching the resumed agent in Rex, picking a task by its Rex label, a real agent continuing the conversation, and `ferry setup HOST` installing ferry over ssh. `test/e2e-local.sh` covers that on kant. Codex sessions (`--agent codex`) aren't seeded by `world.sh new` yet; `test/codex-roundtrip.sh` covers them.

## Conventions

- Each feature's preconditions start from a fresh world, set up as in `../SKILL.md`, with `W` and `R` set and `$R/env` sourced.
- A feature file's steps assume the world state the previous step left. Start a new world to rerun a feature from the top.

## Feature entry contract

Each feature file has an H1, one paragraph on the user-visible behavior, then four H2s in order: `Sub-features`, `How to get to it (user POV)`, `Driving it with world.sh` (starting with `Preconditions:`), and `Gotchas`.
