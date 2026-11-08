# Add Claude permission rules

`ferry setup --claude-rules` adds the Claude Code permission rule ferry's own development needs (`Edit(~/.claude/projects/-tmp-ferry-test-*/**)`) to `~/.claude/settings.json`. It writes through a symlink, keeps key order, saves a backup under `~/.local/state/ferry/backups/`, and does nothing when the rule is already there.

## Sub-features

- `setup-dry` prints the rule it would add and `dry run, nothing written`.
- `setup-write` adds the rule and prints `backup at ...`.
- `setup-idempotent` prints `claude permissions: already set in ...` on a second run.

## How to get to it (user POV)

- `ferry setup --claude-rules` or `ferry setup --claude-rules --dry-run`, from any directory. (`ferry setup HOST` connects to another machine instead; see connect.md.)

## Driving it with world.sh

Preconditions:

- A fresh world. The laptop home has no `.claude/settings.json`.

- **Dry run (`setup-dry`).** Run `$W ferry $R setup --claude-rules --dry-run`. Output `claude permissions: adding Edit(...) to $LAPTOP/.claude/settings.json` and `dry run, nothing written`. `ls $LAPTOP/.claude/settings.json` fails.
- **Write (`setup-write`).** Run `$W ferry $R setup --claude-rules`. Output has `backup at $LAPTOP/.local/state/ferry/backups/claude-settings-<stamp>.json`. `cat $LAPTOP/.claude/settings.json` shows the rule under `permissions.allow`.
- **Again (`setup-idempotent`).** Run `$W ferry $R setup --claude-rules`. Output `claude permissions: already set in ...`, and the file is unchanged.

## Gotchas

- With no settings file, the backup holds `{}`, not a copy of anything.
- On macOS, `/tmp` is a symlink, so the `already set in` line names `/private/tmp/...` while the first two runs name `/tmp/...`.
- To test key-order preservation or the symlink path, write a settings file (or a symlink to one) under `$LAPTOP/.claude/` before the step.
