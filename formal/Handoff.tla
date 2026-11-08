---------------------------- MODULE Handoff ----------------------------
\* A model of one task moving between the laptop and kant: the task lock in
\* main.go (cmdTo, cmdFrom), handoff() and finish() in handoff.go, the record
\* in record.go, FindSession, StageSession, PlaceSession and Liveness in
\* session.go, Launch and StopAgent in rex.go, and execHere in handoff.go.
\*
\* The transcript is one file per place: the main .jsonl that `claude
\* --resume` or `codex resume` needs. Its value is a version number, and
\* lin[v] is the set of turns version v holds. A new turn on a copy makes a
\* new version that holds the copy's turns plus its own, so when both
\* machines take turns on their own copies, the conversation forks. Each
\* move of it in session.go is one os.Rename, so each call below is one
\* atomic step.
\*
\* Since 2b4274b the sending machine keeps its copy after a handoff, so
\* either machine can resume it (PROPOSAL.md, "The sending machine keeps its
\* copy"). The receiver parks a copy it already had before PlaceSession
\* replaces it.
\*
\* The laptop drives every run, under a flock that the kernel drops the
\* moment ferry dies (Ctrl-C, or the laptop crashing; a suspended laptop keeps
\* its ferry and its flock). A call the dead run made on kant can still be
\* running there: `ferry remote` finishes its work and only then fails to
\* write its reply. Pending holds that one call. It holds the task's lock on
\* kant (lockRemoteCall in record.go), and a rerun's first kant call waits
\* for it, so Deliver applies it before the next run starts. You opening the
\* session can still come first.
\*
\* Any run can stop between two steps. Any call to kant can fail, or do its
\* work and lose its reply. Calls on the laptop fail only before doing their
\* work.
\*
\* Left out: --force; you opening an agent while a ferry run is in progress
\* (the liveness checks are for that; one you left open between runs is
\* modeled); more than one call left running on kant at once; a dead call
\* landing partway, between the rerun's own steps; the Attempt write; the
\* restore hook running twice; StageSession finding its stage directory
\* present but empty; and the code, extras and hooks, folded into Work and
\* FinishRestore.

EXTENDS Naturals

\* MaxGen bounds the record's generation, MaxV the number of versions.
\* Faults = FALSE removes every crash and failed call: the happy path.
\* DeadKinds: which calls a dead run can leave running on kant. {} turns
\* that off; DeadCalls is everything the flock allows.
CONSTANTS MaxGen, MaxV, Faults, DeadKinds

DeadCalls == {"put", "stop", "import", "stage", "place", "launch"}

Machines == {"laptop", "kant"}
Other(m) == IF m = "laptop" THEN "kant" ELSE "laptop"
Remote(m) == m = "kant"            \* machine.go Remote.call over SSH
None == "none"
Versions == 1..MaxV

VARIABLES
    copy,      \* copy[m]: version of the live transcript on m, 0 if none
    stageTmp,  \* stageTmp[m]: version in in/<task>/session (transfer target)
    ready,     \* ready[m]: version in in/<task>/ready (StageSession output)
    parkedV,   \* versions PlaceSession moved to parked/<task>-replaced-*
    running,   \* running[m]: agents in ~/.claude/sessions, which Liveness sees
    latest,    \* the newest version ever written
    lin,       \* lin[v]: the turns version v holds
    userOn,    \* userOn[m]: an agent you opened yourself is running on m
    forked,    \* you have had the session open where ferry may replace or abandon it
    rec,       \* the task record on kant
    pc, src, dst,
    runRec,    \* handoff(): rec, this run's copy, read once at the start
    sessV,     \* handoff(): sess, as the version it had (0 = nil)
    instV,     \* handoff()/finish(): installed, as the staged version (0 = nil)
    rerun,     \* this run took the stateLanding branch straight into finish()
    pending    \* a call the dead run left running on kant, or none

vars == <<copy, stageTmp, ready, parkedV, running, userOn, latest, lin, forked, rec, pc, src, dst, runRec, sessV, instV, rerun, pending>>
runVars == <<pc, src, dst, runRec, sessV, instV, rerun>>
turns == <<latest, lin>>
world == <<copy, stageTmp, ready, parkedV, running, userOn, turns>>

NoRec == [owner |-> None, state |-> None, from |-> None, to |-> None, gen |-> 0, srcV |-> 0, instV |-> 0]
NoPending == [kind |-> None, m |-> None, gen |-> 0, new |-> NoRec, v |-> 0]

Init ==
    /\ copy = [m \in Machines |-> IF m = "laptop" THEN 1 ELSE 0]
    /\ stageTmp = [m \in Machines |-> 0]
    /\ ready = [m \in Machines |-> 0]
    /\ parkedV = {}
    /\ running = [m \in Machines |-> 0]
    /\ userOn = [m \in Machines |-> FALSE]
    /\ latest = 1
    /\ lin = [v \in Versions |-> IF v = 1 THEN {1} ELSE {}]
    /\ forked = FALSE      \* ForkedNow is FALSE in the initial state
    \* gen 0 stands for "no record yet"; handoff() then builds one owned by
    \* the source and writes it with Expect 0. Until then the task lives on
    \* the laptop, where it was made.
    /\ rec = [owner |-> "laptop", state |-> "owned", from |-> None, to |-> None,
              gen |-> 0, srcV |-> 0, instV |-> 0]
    /\ pc = "idle" /\ src = None /\ dst = None /\ runRec = NoRec /\ sessV = 0 /\ instV = 0
    /\ rerun = FALSE
    /\ pending = NoPending

Outcomes(m) == IF ~Faults THEN {"ok"}
               ELSE IF Remote(m) THEN {"ok", "failed", "lost"} ELSE {"ok", "failed"}
Applied(o) == o \in {"ok", "lost"}

Stop == pc' = "idle" /\ src' = None /\ dst' = None /\ runRec' = NoRec /\ sessV' = 0 /\ instV' = 0 /\ rerun' = FALSE
Goto(p) == pc' = p /\ UNCHANGED <<src, dst, runRec, sessV, instV, rerun>>
After(o, p) == IF o = "ok" THEN Goto(p) ELSE Stop

\* ---- effects of the calls, shared by the run and by Deliver -------------

\* RecordPut: a compare-and-swap on the stored generation (record.go).
PutEff(expect, new) == IF rec.gen = expect THEN rec' = new ELSE UNCHANGED rec
\* StageSession: empty ready, then move the staged files into it.
StageEff(m) == /\ ready' = [ready EXCEPT ![m] = stageTmp[m]]
               /\ stageTmp' = [stageTmp EXCEPT ![m] = 0]
\* PlaceSession: if ready doesn't exist, it returns nil. If it does, it
\* refuses while Liveness sees an agent with the session loaded. Otherwise
\* ready replaces the live copy, which goes to parked/<task>-replaced-*
\* unless it is the same file.
PlaceRefuses(m) == ready[m] > 0 /\ running[m] > 0
PlaceEff(m) == IF ready[m] > 0 /\ ~PlaceRefuses(m)
                 THEN /\ copy' = [copy EXCEPT ![m] = ready[m]]
                      /\ ready' = [ready EXCEPT ![m] = 0]
                      /\ parkedV' = IF copy[m] \notin {0, ready[m]} THEN parkedV \cup {copy[m]} ELSE parkedV
                 ELSE UNCHANGED <<copy, ready, parkedV>>
\* Launch: `rex new` starts the agent with --resume, and Launch returns once
\* Liveness sees it. An agent that doesn't register in time is killed, and
\* Launch fails. Without a transcript it exits. execHere has the same effect.
LaunchEff(m) ==
    IF copy[m] = 0 THEN UNCHANGED running
    ELSE running' = [running EXCEPT ![m] = @ + 1]
\* StopAgent sends /exit to the agent's pane and waits until no registered
\* process holds the session. A second one keeps it held.
StopOK(m) == running[m] = 1
StopEff(m) == IF StopOK(m) THEN running' = [running EXCEPT ![m] = 0] /\ userOn' = [userOn EXCEPT ![m] = FALSE]
              ELSE UNCHANGED <<running, userOn>>

\* A RecordPut from this run: it expects runRec's generation. "ok" needs the
\* swap to succeed; a failed swap returns "changed underneath this handoff".
Put(o, new, p) ==
    /\ IF Applied(o) THEN PutEff(runRec.gen, new) ELSE UNCHANGED rec
    /\ IF o = "ok" /\ rec.gen = runRec.gen
         THEN pc' = p /\ runRec' = new /\ UNCHANGED <<src, dst, sessV, instV, rerun>>
         ELSE Stop

\* You have the session open where the design lets ferry leave it behind or
\* replace it: on a machine the record doesn't name as owner, which forks it
\* (PROPOSAL.md: "Resuming both does fork the conversation"), or on the
\* receiver of a landing handoff whose staged copy will replace it
\* (PROPOSAL.md: "the old one goes to parked/ first, so turns you added there
\* by hand aren't lost"). forked stays set once this has happened.
ForkedNow == \E m \in Machines :
    userOn[m] /\ (rec.owner # m \/ (rec.state = "landing" /\ ready[m] > 0))

Fork == forked' = (forked \/ ForkedNow')

\* ---- the run -------------------------------------------------------------

\* main.go: l.lockTask (one run at a time), then handoff(): hub.RecordGet,
\* the stateLanding rerun branch, and the refusals, which run before the
\* session step (192fdce). cmdFrom can pull a task with no record
\* (choosePull, 933f57a).
Start(s) ==
    /\ pc = "idle"
    /\ pending = NoPending                       \* lockRemoteCall on kant
    /\ \/ /\ Faults /\ Stop                      \* RecordGet fails
       \/ LET isRerun  == rec.state = "landing" /\ rec.owner = Other(s) /\ rec.from = s
              resuming == rec.state = "handing-off" /\ rec.from = s /\ rec.to = Other(s)
              refused  == rec.gen > 0 /\ ~resuming /\ (rec.state \in {"handing-off", "landing"} \/ rec.owner # s)
          IN IF ~isRerun /\ refused THEN Stop
             ELSE /\ src' = s /\ dst' = Other(s) /\ runRec' = rec
                  /\ pc' = IF isRerun THEN "finishPlace" ELSE "find"
                  /\ rerun' = isRerun
                  \* finish() on a rerun uses rec.Source and rec.Installed.
                  /\ sessV' = IF isRerun THEN rec.srcV ELSE 0
                  /\ instV' = IF isRerun THEN rec.instV ELSE 0
    /\ UNCHANGED <<world, rec, pending>>
    /\ Fork

\* src.FindSession.
Find ==
    /\ pc = "find"
    /\ \/ /\ sessV' = copy[src]
          /\ pc' = IF copy[src] > 0 THEN "dstLive" ELSE "claim"
          /\ UNCHANGED <<src, dst, runRec, instV, rerun>>
       \/ Faults /\ Remote(src) /\ Stop
    /\ UNCHANGED <<world, rec, pending>>
    /\ Fork

\* dst.Liveness: "already has this session loaded". It runs before the agent
\* is stopped (192fdce). Then settle(src) and its liveness.
DstLive ==
    /\ pc = "dstLive"
    /\ IF running[dst] > 0 THEN Stop
       ELSE Goto(IF running[src] > 0 THEN "stopAgent" ELSE "claim")
    /\ UNCHANGED <<world, rec, pending>>
    /\ Fork

\* "stopping the idle agent in rex": only on kant. StopAgent finds the pane
\* from the agent's environment, so it needs no record (933f57a). Then settle
\* and FindSession again, so sess is the version after the last write.
StopAgentStep ==
    /\ pc = "stopAgent"
    /\ IF ~Remote(src)
         THEN Stop /\ UNCHANGED <<running, userOn>> \* "exit the agent, then run this again"
         ELSE \E o \in Outcomes(src):
                /\ IF Applied(o) THEN StopEff(src) ELSE UNCHANGED <<running, userOn>>
                /\ IF o = "ok" /\ StopOK(src)
                     THEN pc' = "claim" /\ sessV' = copy[src] /\ UNCHANGED <<src, dst, runRec, instV, rerun>>
                     ELSE Stop
    /\ UNCHANGED <<copy, stageTmp, ready, parkedV, turns, rec, pending>>
    /\ Fork

ClaimRec == [runRec EXCEPT !.owner = IF runRec.gen = 0 THEN src ELSE runRec.owner,
                           !.state = "handing-off", !.from = src, !.to = dst,
                           !.gen = runRec.gen + 1, !.srcV = sessV, !.instV = 0]

\* --- claim ---: the handing-off RecordPut, which records Source. A resumed
\* claim skips it.
Claim ==
    /\ pc = "claim"
    /\ IF runRec.state = "handing-off" THEN Goto("work") /\ UNCHANGED rec
       ELSE \E o \in Outcomes("kant"): Put(o, ClaimRec, "work")
    /\ UNCHANGED <<world, pending>>
    /\ Fork

\* dehydrate, repo, snapshot, moveRefs, Apply, extras: succeed or return err.
Work ==
    /\ pc = "work"
    /\ \/ Goto(IF sessV > 0 THEN "transfer" ELSE "commit")
       \/ Faults /\ Stop
    /\ UNCHANGED <<world, rec, pending>>
    /\ Fork

\* src.FindSession again, so sess is what is sent, then transfer(src Export,
\* dst Import, Clean) into in/<task>/session.
Transfer ==
    /\ pc = "transfer"
    /\ \/ /\ copy[src] > 0                      \* "disappeared before it was sent"
          /\ stageTmp' = [stageTmp EXCEPT ![dst] = copy[src]]
          /\ pc' = "stage" /\ sessV' = copy[src]
          /\ UNCHANGED <<src, dst, runRec, instV, rerun>>
       \/ copy[src] = 0 /\ Stop /\ UNCHANGED stageTmp
       \/ Faults /\ Stop /\ UNCHANGED stageTmp
    /\ UNCHANGED <<copy, ready, parkedV, running, userOn, turns, rec, pending>>
    /\ Fork

\* dst.StageSession. installed is set only on success. If the stage
\* directory is gone (a dead run's StageSession moved it out and removed
\* it), its WalkDir fails.
Stage ==
    /\ pc = "stage"
    /\ stageTmp[dst] = 0 => (Stop /\ UNCHANGED <<ready, stageTmp>>)
    /\ stageTmp[dst] > 0 => \E o \in Outcomes(dst):
         /\ IF Applied(o) THEN StageEff(dst) ELSE UNCHANGED <<ready, stageTmp>>
         /\ IF o = "ok" THEN pc' = "commit" /\ instV' = stageTmp[dst] /\ UNCHANGED <<src, dst, runRec, sessV, rerun>>
            ELSE Stop
    /\ UNCHANGED <<copy, parkedV, running, userOn, turns, rec, pending>>
    /\ Fork

\* The commit records Source as the session this run sent.
CommitRec == [runRec EXCEPT !.state = "landing", !.owner = dst, !.gen = runRec.gen + 1,
                            !.srcV = sessV, !.instV = instV]

\* --- commit ---: the landing RecordPut. On error: return err.
Commit ==
    /\ pc = "commit"
    /\ \E o \in Outcomes("kant"): Put(o, CommitRec, "finishPlace")
    /\ UNCHANGED <<world, pending>>
    /\ Fork

\* finish(): dst.PlaceSession, if this handoff staged a session. The source
\* keeps its copy (2b4274b).
FinishPlace ==
    /\ pc = "finishPlace"
    /\ IF instV = 0 THEN Goto("finishRestore") /\ UNCHANGED <<copy, ready, parkedV>>
       ELSE \E o \in Outcomes(dst):
              /\ IF Applied(o) THEN PlaceEff(dst) ELSE UNCHANGED <<copy, ready, parkedV>>
              /\ IF o = "ok" /\ ~PlaceRefuses(dst) THEN Goto("finishRestore") ELSE Stop
    /\ UNCHANGED <<stageTmp, running, userOn, turns, rec, pending>>
    /\ Fork

\* finish(): the restore hook. Failing returns err; the record stays landing.
\* Then, with a staged session: on kant, Launch through rex, or print the
\* resume command (--no-launch, no rex). On the laptop, exec the agent here
\* once the record is written (a terminal and no --no-launch), after
\* ExecLive, or print it.
FinishRestore ==
    /\ pc = "finishRestore"
    /\ \/ /\ instV = 0 \/ Remote(dst)
          /\ \/ Goto(IF instV > 0 /\ Remote(dst) THEN "finishLive" ELSE "finishPut")
             \/ Goto("finishPut")
       \/ /\ instV > 0 /\ ~Remote(dst)
          /\ \/ Goto("execLive")
             \/ Goto("finishPut")
       \/ Faults /\ Stop
    /\ UNCHANGED <<world, rec, pending>>
    /\ Fork

\* finish(): "A rerun may find the agent already started" (dst.Liveness,
\* which sees registered agents only).
FinishLive ==
    /\ pc = "finishLive"
    /\ \/ Goto(IF running[dst] > 0 THEN "finishPut" ELSE "finishLaunch")
       \/ Faults /\ Stop
    /\ UNCHANGED <<world, rec, pending>>
    /\ Fork

\* finish(): dst.Liveness before execHere ("already running here; not
\* starting another", 5e2ccc6). An error returns err. If the handoff changed
\* Claude's config, a no at the confirm prints the resume command instead.
ExecLive ==
    /\ pc = "execLive"
    /\ \/ Goto(IF running[dst] > 0 THEN "finishPut" ELSE "finishPutExec")
       \/ /\ running[dst] = 0 /\ Goto("finishPut")
       \/ Faults /\ Stop
    /\ UNCHANGED <<world, rec, pending>>
    /\ Fork

\* finish(): dst.Launch. A failed launch only prints the resume command.
FinishLaunch ==
    /\ pc = "finishLaunch"
    /\ \E o \in Outcomes(dst):
         /\ IF Applied(o) THEN LaunchEff(dst) ELSE UNCHANGED running
         /\ Goto("finishPut")
    /\ UNCHANGED <<copy, stageTmp, ready, parkedV, userOn, turns, rec, pending>>
    /\ Fork

OwnedRec == [runRec EXCEPT !.state = "owned", !.gen = runRec.gen + 1]

\* finish(): the owned RecordPut. On error: return err, so no exec.
FinishPut ==
    /\ pc \in {"finishPut", "finishPutExec"}
    /\ \E o \in Outcomes("kant"):
         IF pc = "finishPutExec" THEN Put(o, OwnedRec, "exec")
         ELSE /\ IF Applied(o) THEN PutEff(runRec.gen, OwnedRec) ELSE UNCHANGED rec
              /\ Stop
    /\ UNCHANGED <<world, pending>>
    /\ Fork

\* execHere: ferry becomes the agent in this terminal, and the task lock goes
\* with ferry's files. LookPath or Chdir can fail first.
Exec ==
    /\ pc = "exec"
    /\ \/ LaunchEff(dst)
       \/ UNCHANGED running
    /\ Stop
    /\ UNCHANGED <<copy, stageTmp, ready, parkedV, userOn, turns, rec, pending>>
    /\ Fork

\* ---- the world around the run -------------------------------------------

\* The laptop's ferry dies between two steps. The flock goes with it.
Crash ==
    /\ Faults /\ pc # "idle" /\ Stop
    /\ UNCHANGED <<world, rec, pending>>
    /\ Fork

\* The laptop's ferry dies while a call on kant is running. That call will
\* still finish on kant, at a time of its choosing. An Import into kant only
\* finishes if the whole stream had arrived; files.go renames each file into
\* the stage directory before it checks the sums.
InFlight ==
    CASE pc = "claim" /\ runRec.state # "handing-off"
           -> [NoPending EXCEPT !.kind = "put", !.m = "kant", !.gen = runRec.gen, !.new = ClaimRec]
      [] pc = "commit"
           -> [NoPending EXCEPT !.kind = "put", !.m = "kant", !.gen = runRec.gen, !.new = CommitRec]
      [] pc \in {"finishPut", "finishPutExec"}
           -> [NoPending EXCEPT !.kind = "put", !.m = "kant", !.gen = runRec.gen, !.new = OwnedRec]
      [] pc = "stopAgent" /\ Remote(src)   -> [NoPending EXCEPT !.kind = "stop", !.m = src]
      [] pc = "transfer" /\ Remote(dst)    -> [NoPending EXCEPT !.kind = "import", !.m = dst, !.v = copy[src]]
      [] pc = "stage" /\ Remote(dst)       -> [NoPending EXCEPT !.kind = "stage", !.m = dst]
      [] pc = "finishPlace" /\ Remote(dst) /\ instV > 0 -> [NoPending EXCEPT !.kind = "place", !.m = dst]
      [] pc = "finishLaunch" -> [NoPending EXCEPT !.kind = "launch", !.m = dst]
      [] OTHER -> NoPending

CrashInFlight ==
    /\ Faults /\ pending = NoPending /\ InFlight.kind \in DeadKinds
    /\ pending' = InFlight
    /\ Stop
    /\ UNCHANGED <<world, rec>>
    /\ Fork

\* The call left running on kant finishes, against the state it finds then.
Deliver ==
    /\ pending # NoPending
    /\ CASE pending.kind = "put"    -> PutEff(pending.gen, pending.new) /\ UNCHANGED <<copy, stageTmp, ready, parkedV, running, userOn>>
         [] pending.kind = "import" -> stageTmp' = [stageTmp EXCEPT ![pending.m] = pending.v]
                                       /\ UNCHANGED <<copy, ready, parkedV, running, userOn, rec>>
         [] pending.kind = "stage"  -> StageEff(pending.m) /\ UNCHANGED <<copy, parkedV, running, userOn, rec>>
         [] pending.kind = "place"  -> PlaceEff(pending.m) /\ UNCHANGED <<stageTmp, running, userOn, rec>>
         [] pending.kind = "launch" -> LaunchEff(pending.m) /\ UNCHANGED <<copy, stageTmp, ready, parkedV, userOn, rec>>
         [] pending.kind = "stop"   -> StopEff(pending.m) /\ UNCHANGED <<copy, stageTmp, ready, parkedV, rec>>
    /\ pending' = NoPending
    /\ UNCHANGED <<turns, runVars>>
    /\ Fork

\* Between runs, you open the session wherever it is live. The agent stays
\* open until you exit it, and Liveness sees it like one ferry started.
Open(m) ==
    /\ pc = "idle" /\ copy[m] > 0 /\ running[m] = 0
    /\ running' = [running EXCEPT ![m] = 1]
    /\ userOn' = [userOn EXCEPT ![m] = TRUE]
    /\ UNCHANGED <<copy, stageTmp, ready, parkedV, turns, rec, runVars, pending>>
    /\ Fork

\* An agent takes a turn, or you exit it.
AgentTurn(m) ==
    /\ running[m] > 0 /\ copy[m] > 0 /\ latest < MaxV
    /\ copy' = [copy EXCEPT ![m] = latest + 1]
    /\ lin' = [lin EXCEPT ![latest + 1] = lin[copy[m]] \cup {latest + 1}]
    /\ latest' = latest + 1
    /\ UNCHANGED <<stageTmp, ready, parkedV, running, userOn, rec, runVars, pending>>
    /\ Fork
AgentExit(m) ==
    /\ pc = "idle"
    /\ \/ /\ userOn[m]
          /\ userOn' = [userOn EXCEPT ![m] = FALSE]
       \/ /\ running[m] > IF userOn[m] THEN 1 ELSE 0
          /\ UNCHANGED userOn
    /\ running' = [running EXCEPT ![m] = @ - 1]
    /\ UNCHANGED <<copy, stageTmp, ready, parkedV, turns, rec, runVars, pending>>
    /\ Fork

Next ==
    \/ \E s \in Machines : Start(s)
    \/ Find \/ DstLive \/ StopAgentStep \/ Claim \/ Work \/ Transfer \/ Stage \/ Commit
    \/ FinishPlace \/ FinishRestore \/ FinishLive \/ ExecLive \/ FinishLaunch \/ FinishPut \/ Exec
    \/ Crash \/ CrashInFlight \/ Deliver
    \/ \E m \in Machines : Open(m) \/ AgentTurn(m) \/ AgentExit(m)

Spec == Init /\ [][Next]_vars

\* Two agents on one machine already break OneAgent, so states past that
\* aren't explored.
Bounds == /\ rec.gen <= MaxGen
          /\ \A m \in Machines : running[m] <= 2

TypeOK ==
    /\ copy \in [Machines -> 0..MaxV]
    /\ ready \in [Machines -> 0..MaxV]
    /\ parkedV \subseteq Versions
    /\ running \in [Machines -> 0..3]
    /\ lin \in [Versions -> SUBSET Versions]
    /\ userOn \in [Machines -> BOOLEAN]
    /\ \A m \in Machines : userOn[m] => running[m] > 0
    /\ rec.state \in {"owned", "handing-off", "landing"}

\* ---- the promises -----------------------------------------------------

\* Where turns survive. Live and staged copies, and a dead run's import on
\* its way into in/<task>/session.
Kept == ({copy[m] : m \in Machines} \cup {ready[m] : m \in Machines}
         \cup {stageTmp[m] : m \in Machines} \cup {pending.v}) \ {0}
Holds(v, S) == \E c \in S : v \in lin[c]

\* Unless you forked the session, ferry never leaves agents running it on
\* both machines. Both would take the Remote Control link from each other
\* (PROPOSAL.md). This replaces NoFork: since 2b4274b both machines keep a
\* resumable copy on purpose, so only running agents are a fork ferry made.
NoTwoAgents == forked \/ ~(running["laptop"] > 0 /\ running["kant"] > 0)

\* Unless you forked the session, every turn is live somewhere, or staged on
\* the owner of a landing record, where rerunning the command puts it live.
\* Turns that end up only in parked/ are turns the user has to dig out by
\* hand. After a fork, one side's turns end up there by design.
NoLostTurns ==
    \/ forked
    \/ \A v \in 1..latest :
         \/ Holds(v, {copy[m] : m \in Machines} \ {0})
         \/ rec.state = "landing" /\ rec.instV > 0 /\ ready[rec.owner] > 0 /\ v \in lin[ready[rec.owner]]

\* No turn is ever destroyed: it is live, staged, or parked. PROPOSAL.md: the
\* receiver's old copy "goes to parked/ first, so turns you added there by
\* hand aren't lost."
NoDestroyedTurns == \A v \in 1..latest : Holds(v, Kept \cup parkedV)

\* When no ferry is running and the record says owned, the owner has the
\* live copy.
OwnerCanResume == (pc = "idle" /\ rec.state = "owned") => copy[rec.owner] > 0

\* One agent process per session on a machine. Two would write the same
\* transcript at once.
OneAgent == \A m \in Machines : running[m] <= 1

\* An agent never runs on a machine without a live transcript; it would write
\* its next turn to a fresh file where the old one was.
AgentHasTranscript == \A m \in Machines : running[m] > 0 => copy[m] > 0

\* ---- reachability: each must be violated, or the model is vacuous --------

ReachRoundTrip == ~(rec.gen >= 6 /\ rec.owner = "laptop" /\ rec.state = "owned")
ReachLaunch == ~(running["kant"] > 0)
ReachExec == ~(pc = "exec" /\ copy[dst] > 0)
ReachStopAgent == ~(pc = "stopAgent" /\ src = "kant")
ReachRerunFinish == ~(pc = "finishPlace" /\ rerun /\ instV > 0)
ReachPlaceRefusal == ~(pc = "finishPlace" /\ instV > 0 /\ PlaceRefuses(dst))
ReachReplacedPark == parkedV = {}
ReachDeadCall == pending = NoPending

=============================================================================
