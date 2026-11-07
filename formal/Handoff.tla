---------------------------- MODULE Handoff ----------------------------
\* A model of one task moving between the laptop and kant: the task lock in
\* main.go (cmdTo, cmdFrom), handoff() and finish() in handoff.go, the record
\* in record.go, and FindSession, StageSession, PlaceSession and Park in
\* session.go.
\*
\* The transcript is one file per place: the main .jsonl that `claude
\* --resume` or `codex resume` needs. Its value is a version number. Every
\* new turn makes a higher version, so two copies differ in size exactly when
\* their versions differ (finish compares sizes). Each move of it in
\* session.go is one os.Rename, so each call below is one atomic step.
\*
\* The laptop drives every run, under a flock that the kernel drops the
\* moment ferry dies. So a rerun can start at once, while a call the dead run
\* made on kant is still running there: `ferry remote` finishes its work and
\* only then fails to write its reply. Pending holds that one call, and
\* Deliver applies it at any later point.
\*
\* Any run can stop between two steps (crash, lid closed, ferry killed). Any
\* call to kant can fail, or do its work and lose its reply. Calls on the
\* laptop fail only before doing their work.
\*
\* Left out: --force; a user resuming while a ferry run is in progress (the
\* liveness checks are for that, and Resume only happens between runs); more
\* than one call left running on kant at once; the Attempt write; and the
\* code, extras and hooks, folded into Work and FinishRestore.

EXTENDS Naturals

\* MaxGen bounds the record's generation, MaxV the number of turns.
\* Faults = FALSE removes every crash and failed call: the happy path.
\* Overlap = TRUE lets a dead run's call on kant finish after the rerun has
\* started, which the flock allows (CrashInFlight, Deliver).
CONSTANTS MaxGen, MaxV, Faults, Overlap

Machines == {"laptop", "kant"}
Other(m) == IF m = "laptop" THEN "kant" ELSE "laptop"
Remote(m) == m = "kant"            \* machine.go Remote.call over SSH
None == "none"

VARIABLES
    copy,      \* copy[m]: version of the live transcript on m, 0 if none
    stageTmp,  \* stageTmp[m]: version in in/<task>/session (transfer target)
    ready,     \* ready[m]: version in in/<task>/ready (StageSession output)
    running,   \* running[m]: agent processes on m with the session loaded
    latest,    \* the newest version ever written
    rec,       \* the task record on kant
    pc, src, dst,
    sessV,     \* handoff()/finish(): sess, as the version it had (0 = nil)
    instV,     \* handoff()/finish(): installed, as the staged version (0 = nil)
    pending    \* a call the dead run left running on kant, or none

vars == <<copy, stageTmp, ready, running, latest, rec, pc, src, dst, sessV, instV, pending>>
runVars == <<pc, src, dst, sessV, instV>>

NoRec == [owner |-> None, state |-> None, from |-> None, to |-> None, gen |-> 0, srcV |-> 0, instV |-> 0]
NoPending == [kind |-> None, m |-> None, gen |-> 0, new |-> NoRec]

Init ==
    /\ copy = [m \in Machines |-> IF m = "laptop" THEN 1 ELSE 0]
    /\ stageTmp = [m \in Machines |-> 0]
    /\ ready = [m \in Machines |-> 0]
    /\ running = [m \in Machines |-> 0]
    /\ latest = 1
    \* gen 0 stands for "no record yet".
    /\ rec = [owner |-> "laptop", state |-> "owned", from |-> None, to |-> None,
              gen |-> 0, srcV |-> 0, instV |-> 0]
    /\ pc = "idle" /\ src = None /\ dst = None /\ sessV = 0 /\ instV = 0
    /\ pending = NoPending

Outcomes(m) == IF ~Faults THEN {"ok"}
               ELSE IF Remote(m) THEN {"ok", "failed", "lost"} ELSE {"ok", "failed"}
Applied(o) == o \in {"ok", "lost"}

Stop == pc' = "idle" /\ src' = None /\ dst' = None /\ sessV' = 0 /\ instV' = 0
Goto(p) == pc' = p /\ UNCHANGED <<src, dst, sessV, instV>>
\* The call's outcome decides whether the run goes on to p or returns err.
After(o, p) == IF o = "ok" THEN Goto(p) ELSE Stop

\* ---- effects of the calls, shared by the run and by Deliver -------------

\* hub.RecordPut: a compare-and-swap on the generation (record.go).
PutEff(expect, new) == IF rec.gen = expect THEN rec' = new ELSE UNCHANGED rec
\* StageSession: empty ready, then move the staged files into it.
StageEff(m) == /\ ready' = [ready EXCEPT ![m] = stageTmp[m]]
               /\ stageTmp' = [stageTmp EXCEPT ![m] = 0]
\* Park(As: g<gen>): the live copy moves to parked/.
ParkEff(m) == copy' = [copy EXCEPT ![m] = 0]
\* PlaceSession: if ready exists, it replaces any live copy.
PlaceEff(m) == IF ready[m] > 0
                 THEN /\ copy' = [copy EXCEPT ![m] = ready[m]]
                      /\ ready' = [ready EXCEPT ![m] = 0]
                 ELSE UNCHANGED <<copy, ready>>
\* Launch: rex starts the agent with --resume. Without a transcript it exits.
LaunchEff(m) == IF copy[m] > 0 THEN running' = [running EXCEPT ![m] = @ + 1]
                ELSE UNCHANGED running
\* StopAgent sends /exit to the one recorded rex session and waits until no
\* process holds the session. A second agent keeps it held, so it fails.
StopOK(m) == running[m] = 1
StopEff(m) == IF StopOK(m) THEN running' = [running EXCEPT ![m] = 0] ELSE UNCHANGED running

\* ---- the run -------------------------------------------------------------

\* main.go: l.lockTask (one run at a time), then handoff(): hub.RecordGet.
Start(s) ==
    /\ pc = "idle"
    /\ s = "kant" => rec.gen > 0                 \* cmdFrom: findTask needs a record
    /\ \/ /\ Faults /\ Stop                      \* RecordGet fails
       \/ LET rerun == rec.state = "landing" /\ rec.owner = Other(s) /\ rec.from = s IN
          /\ src' = s /\ dst' = Other(s)
          /\ pc' = IF rerun THEN "finishCheck" ELSE "find"
          \* finish() on a rerun uses rec.Source and rec.Installed.
          /\ sessV' = IF rerun THEN rec.srcV ELSE 0
          /\ instV' = IF rerun THEN rec.instV ELSE 0
    /\ UNCHANGED <<copy, stageTmp, ready, running, latest, rec, pending>>

\* src.FindSession, then settle(src) and its liveness.
Find ==
    /\ pc = "find"
    /\ \/ /\ sessV' = copy[src]
          /\ pc' = IF copy[src] > 0 /\ running[src] > 0 THEN "stopAgent" ELSE "dstLive"
          /\ UNCHANGED <<src, dst, instV>>
       \/ Faults /\ Remote(src) /\ Stop
    /\ UNCHANGED <<copy, stageTmp, ready, running, latest, rec, pending>>

\* "stopping the idle agent in rex": only on kant, only with a record. Then
\* settle and FindSession again, so sess is the version after the last write.
StopAgentStep ==
    /\ pc = "stopAgent"
    /\ IF ~Remote(src) \/ rec.gen = 0
         THEN Stop /\ UNCHANGED running         \* "exit the agent, then run this again"
         ELSE \E o \in Outcomes(src):
                /\ IF Applied(o) THEN StopEff(src) ELSE UNCHANGED running
                /\ IF o = "ok" /\ StopOK(src)
                     THEN pc' = "dstLive" /\ sessV' = copy[src] /\ UNCHANGED <<src, dst, instV>>
                     ELSE Stop
    /\ UNCHANGED <<copy, stageTmp, ready, latest, rec, pending>>

\* dst.Liveness: "already has this session loaded".
DstLive ==
    /\ pc = "dstLive"
    /\ IF sessV > 0 /\ running[dst] > 0 THEN Stop ELSE Goto("claim")
    /\ UNCHANGED <<copy, stageTmp, ready, running, latest, rec, pending>>

ClaimRec == [rec EXCEPT !.state = "handing-off", !.from = src, !.to = dst,
                        !.gen = rec.gen + 1, !.srcV = sessV, !.instV = 0]

\* --- claim ---: the refusals, resuming, and the handing-off RecordPut.
Claim ==
    /\ pc = "claim"
    /\ LET resuming == rec.state = "handing-off" /\ rec.from = src /\ rec.to = dst
           refused  == ~resuming /\ (rec.state \in {"handing-off", "landing"} \/ rec.owner # src)
       IN  IF refused THEN Stop /\ UNCHANGED rec
           ELSE IF resuming THEN Goto("work") /\ UNCHANGED rec
           ELSE \E o \in Outcomes("kant"):
                  /\ IF Applied(o) THEN PutEff(rec.gen, ClaimRec) ELSE UNCHANGED rec
                  /\ After(o, "work")
    /\ UNCHANGED <<copy, stageTmp, ready, running, latest, pending>>

\* dehydrate, repo, snapshot, moveRefs, Apply, extras: succeed or return err.
Work ==
    /\ pc = "work"
    /\ \/ Goto(IF sessV > 0 THEN "transfer" ELSE "commit")
       \/ Faults /\ Stop
    /\ UNCHANGED <<copy, stageTmp, ready, running, latest, rec, pending>>

\* transfer(src Export, dst Import) into in/<task>/session. It streams from
\* the laptop or to it, so it cannot finish after the laptop's ferry dies.
Transfer ==
    /\ pc = "transfer"
    /\ \/ /\ stageTmp' = [stageTmp EXCEPT ![dst] = copy[src]]
          /\ Goto("stage")
       \/ Faults /\ Stop /\ UNCHANGED stageTmp
    /\ UNCHANGED <<copy, ready, running, latest, rec, pending>>

\* dst.StageSession. installed is set only on success. If the stage
\* directory is gone (a dead run's StageSession moved it out and removed
\* it), its WalkDir fails. Left out: the moment when it exists but is empty.
Stage ==
    /\ pc = "stage"
    /\ stageTmp[dst] = 0 => (Stop /\ UNCHANGED <<ready, stageTmp>>)
    /\ stageTmp[dst] > 0 => \E o \in Outcomes(dst):
         /\ IF Applied(o) THEN StageEff(dst) ELSE UNCHANGED <<ready, stageTmp>>
         /\ IF o = "ok" THEN pc' = "commit" /\ instV' = stageTmp[dst] /\ UNCHANGED <<src, dst, sessV>>
            ELSE Stop
    /\ UNCHANGED <<copy, running, latest, rec, pending>>

CommitRec == [rec EXCEPT !.state = "landing", !.owner = dst, !.gen = rec.gen + 1, !.instV = instV]

\* --- commit ---: the landing RecordPut. On error: return err.
Commit ==
    /\ pc = "commit"
    /\ \E o \in Outcomes("kant"):
         /\ IF Applied(o) THEN PutEff(rec.gen, CommitRec) ELSE UNCHANGED rec
         /\ After(o, "finishCheck")
    /\ UNCHANGED <<copy, stageTmp, ready, running, latest, pending>>

\* finish(): FindSession and Liveness on the source, then the size check.
FinishCheck ==
    /\ pc = "finishCheck"
    /\ IF sessV = 0 THEN Goto("finishPlace")
       ELSE \/ /\ IF copy[src] > 0 /\ (running[src] > 0 \/ copy[src] # sessV)
                    THEN Stop                   \* "exit it" / "changed after it was handed off"
                    ELSE Goto("finishPark")
            \/ Faults /\ Remote(src) /\ Stop
    /\ UNCHANGED <<copy, stageTmp, ready, running, latest, rec, pending>>

\* finish(): src.Park(As: g<gen>).
FinishPark ==
    /\ pc = "finishPark"
    /\ \E o \in Outcomes(src):
         /\ IF Applied(o) THEN ParkEff(src) ELSE UNCHANGED copy
         /\ After(o, "finishPlace")
    /\ UNCHANGED <<stageTmp, ready, running, latest, rec, pending>>

\* finish(): dst.PlaceSession, if this handoff staged a session.
FinishPlace ==
    /\ pc = "finishPlace"
    /\ IF instV = 0 THEN Goto("finishRestore") /\ UNCHANGED <<copy, ready>>
       ELSE \E o \in Outcomes(dst):
              /\ IF Applied(o) THEN PlaceEff(dst) ELSE UNCHANGED <<copy, ready>>
              /\ After(o, "finishRestore")
    /\ UNCHANGED <<stageTmp, running, latest, rec, pending>>

\* finish(): the restore hook. Failing returns err; the record stays landing.
\* Launch happens only on kant, and only if a session was staged.
FinishRestore ==
    /\ pc = "finishRestore"
    /\ \/ Goto(IF instV > 0 /\ Remote(dst) THEN "finishLive" ELSE "finishPut")
       \/ Faults /\ Stop
    /\ UNCHANGED <<copy, stageTmp, ready, running, latest, rec, pending>>

\* finish(): "A rerun may find the agent already started" (dst.Liveness).
FinishLive ==
    /\ pc = "finishLive"
    /\ \/ Goto(IF running[dst] > 0 THEN "finishPut" ELSE "finishLaunch")
       \/ Faults /\ Stop
    /\ UNCHANGED <<copy, stageTmp, ready, running, latest, rec, pending>>

\* finish(): dst.Launch. A failed launch only prints the resume command.
FinishLaunch ==
    /\ pc = "finishLaunch"
    /\ \E o \in Outcomes(dst):
         /\ IF Applied(o) THEN LaunchEff(dst) ELSE UNCHANGED running
         /\ Goto("finishPut")
    /\ UNCHANGED <<copy, stageTmp, ready, latest, rec, pending>>

\* finish(): the owned RecordPut.
FinishPut ==
    /\ pc = "finishPut"
    /\ \E o \in Outcomes("kant"):
         /\ IF Applied(o) THEN PutEff(rec.gen, [rec EXCEPT !.state = "owned", !.gen = rec.gen + 1])
            ELSE UNCHANGED rec
         /\ Stop
    /\ UNCHANGED <<copy, stageTmp, ready, running, latest, pending>>

\* ---- the world around the run -------------------------------------------

\* The laptop's ferry dies between two steps. The flock goes with it.
Crash ==
    /\ Faults /\ pc # "idle" /\ Stop
    /\ UNCHANGED <<copy, stageTmp, ready, running, latest, rec, pending>>

\* The laptop's ferry dies while a call on kant is running. That call will
\* still finish on kant, at a time of its choosing.
InFlight ==
    CASE pc = "claim" /\ rec.state = "owned" /\ rec.owner = src
           -> [kind |-> "put", m |-> "kant", gen |-> rec.gen, new |-> ClaimRec]
      [] pc = "commit" -> [kind |-> "put", m |-> "kant", gen |-> rec.gen, new |-> CommitRec]
      [] pc = "finishPut" -> [kind |-> "put", m |-> "kant", gen |-> rec.gen,
                               new |-> [rec EXCEPT !.state = "owned", !.gen = rec.gen + 1]]
      [] pc = "stopAgent" /\ Remote(src) /\ rec.gen > 0 -> [NoPending EXCEPT !.kind = "stop", !.m = src]
      [] pc = "stage" /\ Remote(dst)       -> [NoPending EXCEPT !.kind = "stage", !.m = dst]
      [] pc = "finishPark" /\ Remote(src)  -> [NoPending EXCEPT !.kind = "park", !.m = src]
      [] pc = "finishPlace" /\ Remote(dst) /\ instV > 0 -> [NoPending EXCEPT !.kind = "place", !.m = dst]
      [] pc = "finishLaunch" -> [NoPending EXCEPT !.kind = "launch", !.m = dst]
      [] OTHER -> NoPending

CrashInFlight ==
    /\ Faults /\ Overlap /\ pending = NoPending /\ InFlight # NoPending
    /\ pending' = InFlight
    /\ Stop
    /\ UNCHANGED <<copy, stageTmp, ready, running, latest, rec>>

\* The call left running on kant finishes, against the state it finds then.
Deliver ==
    /\ pending # NoPending
    /\ CASE pending.kind = "put"    -> PutEff(pending.gen, pending.new) /\ UNCHANGED <<copy, stageTmp, ready, running>>
         [] pending.kind = "stage"  -> StageEff(pending.m) /\ UNCHANGED <<copy, running, rec>>
         [] pending.kind = "park"   -> ParkEff(pending.m) /\ UNCHANGED <<stageTmp, ready, running, rec>>
         [] pending.kind = "place"  -> PlaceEff(pending.m) /\ UNCHANGED <<stageTmp, running, rec>>
         [] pending.kind = "launch" -> LaunchEff(pending.m) /\ UNCHANGED <<copy, stageTmp, ready, rec>>
         [] pending.kind = "stop"   -> StopEff(pending.m) /\ UNCHANGED <<copy, stageTmp, ready, rec>>
    /\ pending' = NoPending
    /\ UNCHANGED <<latest, runVars>>

\* Between runs, you resume the session wherever it is live, add turns, exit.
Resume(m) ==
    /\ pc = "idle" /\ copy[m] > 0 /\ running[m] = 0 /\ latest < MaxV
    /\ copy' = [copy EXCEPT ![m] = latest + 1]
    /\ latest' = latest + 1
    /\ UNCHANGED <<stageTmp, ready, running, rec, runVars, pending>>

\* An agent ferry launched on kant takes a turn, or you exit it.
AgentTurn(m) ==
    /\ running[m] > 0 /\ copy[m] > 0 /\ latest < MaxV
    /\ copy' = [copy EXCEPT ![m] = latest + 1]
    /\ latest' = latest + 1
    /\ UNCHANGED <<stageTmp, ready, running, rec, runVars, pending>>
AgentExit(m) ==
    /\ pc = "idle" /\ running[m] > 0
    /\ running' = [running EXCEPT ![m] = @ - 1]
    /\ UNCHANGED <<copy, stageTmp, ready, latest, rec, runVars, pending>>

Next ==
    \/ \E s \in Machines : Start(s)
    \/ Find \/ StopAgentStep \/ DstLive \/ Claim \/ Work \/ Transfer \/ Stage \/ Commit
    \/ FinishCheck \/ FinishPark \/ FinishPlace \/ FinishRestore \/ FinishLive
    \/ FinishLaunch \/ FinishPut
    \/ Crash \/ CrashInFlight \/ Deliver
    \/ \E m \in Machines : Resume(m) \/ AgentTurn(m) \/ AgentExit(m)

Spec == Init /\ [][Next]_vars

Bounds == rec.gen <= MaxGen

TypeOK ==
    /\ copy \in [Machines -> 0..MaxV]
    /\ ready \in [Machines -> 0..MaxV]
    /\ running \in [Machines -> 0..2]
    /\ rec.state \in {"owned", "handing-off", "landing"}

\* ---- the promises -----------------------------------------------------

\* README/PROPOSAL: at no point can both machines resume it.
NoFork == ~(copy["laptop"] > 0 /\ copy["kant"] > 0)

\* The newest turns are always live somewhere, or staged on the owner of a
\* landing record, where rerunning the command puts them live. Turns that
\* end up only in parked/ are turns the user has to dig out by hand.
NoLostTurns ==
    \/ \E m \in Machines : copy[m] = latest
    \/ rec.state = "landing" /\ rec.instV > 0 /\ ready[rec.owner] = latest

\* When no ferry is running and the record says owned, the owner has the
\* live copy.
OwnerCanResume == (pc = "idle" /\ rec.state = "owned") => copy[rec.owner] > 0

\* One agent process per session on a machine. Two would write the same
\* transcript at once.
OneAgent == \A m \in Machines : running[m] <= 1

\* An agent never runs on a machine whose transcript ferry moved away; it
\* would write its next turn to a fresh file where the old one was.
AgentHasTranscript == \A m \in Machines : running[m] > 0 => copy[m] > 0

=============================================================================
