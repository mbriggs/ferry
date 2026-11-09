---
name: verify-handoff
description: TLA+ check that a handoff never runs two agents on one session, loses or destroys turns, or leaves the owner without a copy. Use when a change touches handoff(), finish(), the lock, record.go, session.go transcript moves, or Launch/StopAgent.
---

# Verify the handoff

`formal/Handoff.tla` models one task moving between the laptop and kant: the task lock, `handoff()`, `finish()`, the record, the calls that move the transcript, the agents ferry starts and stops, and agents you open yourself between runs. Since 2b4274b the sending machine keeps its copy, so both machines can resume the session on purpose (README.md, "The sender keeps its copy of the session"). TLC explores every order of steps up to `MaxGen = 12` record generations, which is two round trips (each handoff writes three: claim, commit, finish), and `MaxV = 3` transcript versions (two new turns). Each version holds a set of turns (`lin`), so the model can tell when the two copies fork.

`forked` is set once you have had the session open where the design lets ferry leave it behind or replace it: on a machine the record doesn't name as owner, or on the receiver of a `landing` handoff whose staged copy will replace it. README.md accepts both: "Resuming both forks the conversation", and the receiver's old copy "goes to `~/.local/state/ferry/parked/` first, so turns you added there aren't lost". The model checks six promises:

- **NoTwoAgents.** Unless you forked the session, ferry never leaves agents running it on both machines. Both would take the Remote Control link from each other. It replaces NoFork ("at no point can both machines resume it"), which 2b4274b dropped on purpose.
- **NoLostTurns.** Unless you forked the session, every turn is in a live copy, or staged in `in/<task>/ready` on the owner of a `landing` record, where a rerun puts it live. Otherwise it survives only in `~/.local/state/ferry/parked/`, and you have to dig it out.
- **NoDestroyedTurns.** Every turn is live, staged, or parked. Nothing deletes a transcript.
- **OwnerCanResume.** When no ferry is running and the record says `owned`, the owner has a live copy. Otherwise ferry launches an agent with no session, and the record points at a machine with nothing to resume.
- **OneAgent.** At most one agent process per machine has the session loaded. Two would write the same transcript at once.
- **AgentHasTranscript.** No agent runs on a machine without a live transcript. Since 2b4274b no step removes a live copy, so this can only fail if a change brings one back.

`TypeOK` is a sanity check.

## Does the change touch it?

The model covers this code, as of the sync commit below. Run `git diff <sync commit> -- handoff.go record.go session.go machine.go main.go rex.go rsync.go` and compare the result against this table. `formal/check.sh` prints a `WARN` line when that diff isn't empty.

| Model action | Code |
|---|---|
| `Start` | `main.go` `cmdTo` (`chooseTo`)/`cmdFrom` (`unlockDeadClaude`, which the model leaves out, then `l.lockTask`, `remote.Task`; `choosePull` can pick a task with no record), then `handoff()`: `hub.RecordGet` into `m.rec` (`runRec`), the `stateLanding` rerun branch, `m.mayHandOff` (unfinished handoff, other owner), and `m.checkReceiver` (a different repo, or receiver work the handoff would overwrite; saving that work to a branch touches only git), which run before `m.stopAgent`. `--check` returns before `stopAgent` stops anything, which the model folds into a stop with no effect. Waits for a `Locked` dead call, because the first kant call waits on `lockRemoteCall` (`record.go`, taken in `serveRemote`). A dead session rsync holds no lock |
| `RerunFind` | the rerun branch's `m.resendIfResumed`: `src.FindSession` for `rec.Source`. If the copy grew, `src.Liveness` refuses while an agent has it open, else `m.sendSession` (`Transfer`, `Stage`) and the `landing` `m.save()` (`Commit`) record the newer copy |
| `Find` | `m.stopAgent`: `src.FindSession` (with `KnownID`, the session the task last moved with) |
| `DstLive` | `m.stopAgent`: `dst.Liveness`, "already has this session loaded", before the agent is stopped |
| `StopAgentStep` | `m.stopAgent`: `settle`, then on kant `src.StopAgent` (the pane comes from the agent's environment, so it needs no record; a Claude in its own worktree gets SIGTERM instead of `/exit`; once the agent exits it closes the record's Rex session if no blocks are left, which the model leaves out), `settle`, and `FindSession` again. On the laptop it refuses: "exit the agent, then run this again", or "close its pane" for a Claude in its own worktree |
| `Claim` | `m.claim`: the `handing-off` `m.save()` that records `Source`. A resumed claim skips it |
| `Work` | `m.dehydrate`, `m.sendCode` (repo, snapshot, `moveRefs`, the `Attempt` `m.save()`, `Apply`, `syncRemotes`), `m.sendExtras` (rsync), `m.sendGitHooks` |
| `Transfer` | `m.stageSession`: the second `src.FindSession` ("disappeared before it was sent"), then `m.sendSession`: `rsyncTo` (`rsync.go`) into `in/<task>/session`, run on the laptop as a child of ferry |
| `Stage`, `StageEff`, `SizeMatches` | `dst.StageSession` into `in/<task>/ready`. It first sums the staged files' sizes and refuses, with `ready` untouched, unless that is the `Size` of the session as sent; an older copy, or none, fails. The model counts a size in turns. Then it empties `ready` and moves the files in. It removes the stage directory either way. `installed` is set only on success |
| `Commit` | `m.commit`: the `landing` `m.save()`, which sets `rec.Source` to the session sent and returns err on failure |
| `FinishPlace`, `PlaceEff` | `finish()`: `dst.PlaceSession`: nil if `ready` is gone, refuses while `Liveness` sees the session loaded, else parks the receiver's copy to `parked/<task>-replaced-*` and moves `ready` in. The source keeps its copy |
| `FinishRestore` | the restore hook, then the branch: `Launch` on kant, exec here on the laptop (a terminal, no `--no-launch`), or print the resume command |
| `ExecLive` | on the laptop, `dst.Liveness` ("already running here; not starting another"), then the confirm when the handoff changed Claude's config (`configChanges`). A no prints the resume command |
| `FinishLive`, `FinishLaunch` | `dst.Liveness` ("already running"), then `dst.Launch` (`rex.go`: refuses if `Liveness` sees the session loaded, then returns once `Liveness` sees the agent, or kills it and fails). A failed launch returns err with the record still `landing`; an untrusted folder starts nothing and goes on to the `owned` put |
| `FinishPut`, `Exec`, `ExecEff` | the `owned` RecordPut, then `execHere` on the laptop |
| `Put`, `PutEff` | `RecordPut`'s compare-and-swap against this run's generation |
| `StageEff`, `PlaceEff`, `LaunchEff`, `StopEff` | what `session.go` and `rex.go` do when the call runs |
| `running`, `userOn` | agents `Liveness` sees: `~/.claude/sessions/<pid>.json`, or Codex's thread lock. `userOn` marks one you opened |
| `Outcomes`, `Remote` | `machine.go` `Remote.call`: a kant call can fail, or do its work and lose its reply |
| `CrashInFlight`, `Deliver`, `Locked` | a `ferry remote` call still running on kant after the laptop's ferry died, which holds `lockRemoteCall` until it finishes. Or the session `rsync` from `sendSession`, on either machine: Ctrl-C stops it with ferry, but a file kant's receiver already has is still renamed in, and when ferry alone dies (kill, the OOM killer) rsync runs on to the end. The laptop's rsync inherits the task lock (`runHoldingLock`), so a rerun on the laptop can't start until it exits, but kant's receiving rsync holds nothing and can still rename a file in after its sender is gone. The model keeps the wider overlap, which is still sound |
| `Open`, `AgentTurn`, `AgentExit` | you opening the session between runs and leaving it open, and any agent taking a turn |

If the diff changes the order of these calls, what a failure does (return, or print and continue), which machine a call runs on, what a call moves, what `Liveness` can see, when `Launch` returns, when ferry starts an agent, or what either lock excludes, the change touches the model. Changes to path rewriting, hook prompts, extras planning, git snapshots, rsync's flags, ssh options, session picking in `pick.go`, or step timing do not.

**Sync commit: `04459bcf801af070504c71775043d0bb5e7e23e3`.** At that commit, the model matches the code in the table. Move the sync commit forward only after you have reread every row against the code at the new commit, and confirmed that the model describes it.

## Run it

```
formal/check.sh
```

It takes about a minute. It needs Java 11 or later and mise, and it installs TLC 1.7.4 through mise, pinned by checksum, on the first run. Set `TLA2TOOLS=/path/to/tla2tools.jar` to skip mise. `formal/check.sh OneAgent` checks one invariant.

There are three modes:

- **happy**: no crash and no failed call.
- **faults**: crashes between steps, failed calls, lost replies, and agents you open between runs.
- **overlap**: faults, plus one call that a dead run left running on kant, finishing before the next run starts.

In each mode, a promise must hold, unless `formal/known-findings` lists it for that mode. A listed promise must fail as the code is, and must hold once the switches on its line remove its cause. That second run is what keeps a known finding from hiding a new one. A switch is `-KIND`, which drops one kind of dead call (`put`, `stop`, `import`, `stage`, `place`, `launch`). When a new finding needs a switch that this doesn't describe, add a model constant that defaults to what the code does, and teach `check()` in `check.sh` to set it.

Each `Reach*` check must fail in its mode. A failing reach check shows that the model gets to a round trip, a launch on kant, an exec on the laptop, a stopped agent, a parked replaced copy, a rerun that sends the session again, a rerun into `finish()`, a `PlaceSession` refusal, a dead call, or a `StageSession` that finds a copy of the wrong size. If one passes, the model can't get there, and every pass in that mode means less.

Exit 0 means all of the above. The output lines mean:

- `PASS`, `KNOWN`, `REACH`: as described above.
- `FAIL ... violated`: a new way to break a promise. Read the trace printed under it.
- `FAIL ... still fails with ...`: a known finding has a second cause. Read the pinned trace.
- `FAIL ... never reached`: the model is vacuous there.
- `FIXED?`: a known finding now holds. If you fixed it on purpose, delete its line from `known-findings`. If a `never reached` line appears in the same run, the model lost a path, and nothing is fixed.
- `ERROR`: TLC didn't finish, usually a syntax error in the spec. Read `formal/out/<mode>-<check>.out`.

Read every result with `/statechart`. It turns each violation into its shortest path in plain words, and when the check passes, it draws how a task moves between states. Give it `formal/Handoff.tla` and a config from `formal/out/`.

Every action ends with `/\ Fork`, which keeps `forked` current. A new action needs it too, as its last conjunct, because TLC assigns primed variables in order.

## Bug or modeling error?

Tell the trace as steps in ferry's words, such as "the laptop's ferry dies while kant runs `Launch`, you open the session on kant, and `Launch` starts a second agent". Then check each step against the code in the table. If every step is something the code can do, it's a bug. Report it with that story, and with how wide the window is in practice. Don't change the model or the invariant to make it pass. If a step is something the code can't do, such as a call that can't fail, a call that fails differently, or an order the code doesn't allow, the model is wrong. Fix the action, and cite the line that shows it. An invariant has to judge what the code would do, so give it the same definitions the actions use, as `Kept` and `ForkedNow` do.

## Update it for an intended change

Mirror the change in the matching action, keeping one action per call that can fail on its own, and keep the comment that names the code. Run `formal/check.sh`. A change that fixes a known finding shows as `FIXED?`: delete that line. When the fix is one that a switch models, delete the switch too, once no line uses it. A change that adds a finding needs a line with the switches that remove its cause, and if no switch can describe the cause, add one that defaults to the code's behavior. Then move the sync commit.

## Current findings

None. `known-findings` lists no line.

Fixed or gone since the first version of this model:

- A session rsync left running by a dead `ferry to` could rename an older copy over the one the rerun had just sent, after you had added a turn on the laptop. `StageSession` staged the older copy, and the agent on kant resumed without your turn (since 91684d8, which moved the session from a locked `ferry remote import` to rsync). c5ee2f8 made the laptop's rsync hold the task lock, which shut out a rerun while it runs, but kant's receiving rsync can still rename a file in after its sender is gone. `StageSession` now refuses a staged copy whose size isn't the one sent, before it touches `ready`. Agents only append, so an older copy is smaller. Two forked copies of the same size would still pass, and the model allows that too.

- `Launch` returned before the agent registered (`InstantRegister`). Now it waits.
- `finish()` compared the source against a stale session (`CompareStaged`). The size check is now gone entirely (2b4274b), and `SizeCheckFair` went with it.
- A dead call on kant could land in the middle of a rerun. Now `ferry remote` holds `lockRemoteCall` for each call on a task.
- A rerun of `ferry from` could exec a second agent on the laptop next to one you had opened there after the restore hook failed. `finish()` now asks `Liveness` before `execHere` (5e2ccc6), and the `+execlive` switch went with it.
- A dead `Launch` on kant could start a second agent next to one you opened before `rex new` ran. `Launch` now asks `Liveness` first. Opening one in the milliseconds between that check and `rex new` is still possible, and the model leaves it out with the other opens during a run.
- Turns you added on the source after a failed `ferry to`, while the dead commit RecordPut was still landing, stayed behind, and the next `ferry from kant` parked them. A rerun now sends the source's copy again when it has grown since the commit recorded it (`resendIfResumed`), and refuses while an agent still has it open.
- A dead `Park` on kant could park turns you had just added. `finish()` no longer parks the source (2b4274b), so the call and its finding are gone.

## What it leaves out

- The transcript is one file, the main `.jsonl`, and each call that moves it is one `os.Rename`. Subagent directories, file history, and the project memory folder aren't modeled.
- A dead run's call lands all at once. A `ferry remote` call lands before the next run starts; the session rsync can land at any step. The model doesn't split a call against your own steps, and while one dead call is pending, a second crash leaves nothing running.
- You open the session only between runs. It doesn't model `--force`, or you opening an agent while a ferry run is in progress (the liveness checks are for that). It also leaves out more than one call left running on kant at once, the restore hook running twice, the difference between Codex's lock and Claude's registry file, `StageSession` finding its stage directory present but empty, and an agent writing during the transfer.
- `pick.go` (which task and session `ferry from` picks) and `FindSession`'s choice between the worktree's newest session and the one the task last moved with. The model has one session.
- A machine with copies of one Claude transcript in two project folders, which Claude leaves when it refiles a worktree session (6f2e31c). `FindSession` by id takes the newest, and `PlaceSession` parks the others. The model has one copy per machine.
- Code, extras, git hooks and the restore and dehydrate hooks are folded into `Work` and `FinishRestore`.
- `known-findings` holds at the default bounds. `MAXGEN=18 formal/check.sh` checks three round trips. A new failure there means two round trips hid something: read it before you change any line.
