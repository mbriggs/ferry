# ferry

Hand a coding task, meaning a git worktree under `~/src` plus its Claude Code or Codex session, from the laptop to kant and back. The agent resumes the full conversation on the other side. See [PROPOSAL.md](PROPOSAL.md) for the design.

```
ferry to kant                 # in the worktree, after exiting the agent
ferry from kant fix-auth      # bring it back
ferry ls kant
```

## Setup

1. On kant, enable SSH over the tailnet and limit it to your devices with a tailnet ACL:

   ```
   sudo tailscale set --ssh
   ```

2. Install the binary on both machines at `~/.local/bin/ferry`, the path the laptop runs over SSH. On kant, run `make install`. For the Mac, run `make dist` on kant and copy `dist/ferry-darwin-arm64` to the Mac's `~/.local/bin/ferry`.

3. On the laptop, tell ferry how to reach kant. Your Mac username differs from kant's, so name the user:

   ```
   mkdir -p ~/.config/ferry && echo "kant mbriggs@kant" > ~/.config/ferry/hosts
   ```

4. Check the connection with `ferry ls kant`. It should print `no tasks`.

## First real test

Use a scratch repo:

1. On the laptop, run `mkdir -p ~/src/ferry-trial && cd ~/src/ferry-trial && git init`, commit one file, and leave one uncommitted.
2. Run `claude`, tell it a codeword, then `/exit`.
3. Run `ferry to kant`. It should finish with `launch  kant: rex session "ferry-trial" running claude --resume ...`.
4. Attach to that Rex session on kant and ask for the codeword.
5. Run `ferry from kant ferry-trial` on the laptop, then run the `resume:` command it prints.

## Tests

- `make test` runs a round trip between two fake homes. The remote side runs as a real `ferry remote` subprocess.
- `test/e2e-local.sh` runs a real Claude session on kant through Rex, with a fake laptop home. Run `test/e2e-local.sh clean` afterwards.
- `test/liveness.sh` rechecks the agent internals ferry depends on. Run it after upgrading Claude Code or Codex.
