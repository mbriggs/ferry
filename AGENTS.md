# ferry

Treat the primary checkout as read-only: read, search, build, and run tests there freely. Before the first file edit or commit, create a linked worktree with the `worktree` skill and make the change there, unless the user asks for it in the primary checkout. If the working directory is already under `.claude/worktrees/`, work there.
