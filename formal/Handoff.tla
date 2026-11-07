---------------------------- MODULE Handoff ----------------------------
\* A model of one task moving between the laptop and kant through
\* handoff() and finish() in handoff.go, with the record (record.go) and the
\* park/unpark/install operations (session.go).
\*
\* The transcript is modeled as one file per machine: the main .jsonl that
\* `claude --resume` or `codex resume` needs. Every move of it in session.go
\* is one os.Rename, so each operation below is one atomic step.
\*
\* The laptop drives every run. Any run can stop between two steps (the
\* laptop crashes, the lid closes, ferry is killed), and the next run starts
\* from whatever was left. Any call to kant can fail. A call to kant can also
\* do its work and still return an error (the SSH connection drops before the
\* reply arrives). Calls on the laptop fail only before doing their work.
\*
\* Left out: --force, two ferry runs at once (the task lock in lockTask
\* prevents it), running agent processes (liveness checks pass), the
\* Attempt write (it changes no owner, state, or file), and the code, extras
\* and hooks, which are folded into the one step Work.

EXTENDS Naturals

\* MaxGen bounds the record's generation, so the number of handoffs.
\* Faults = FALSE removes every crash and failed call: the happy path.
CONSTANTS MaxGen, Faults

Machines == {"laptop", "kant"}
Other(m) == IF m = "laptop" THEN "kant" ELSE "laptop"
\* The laptop runs ferry, so an operation on kant goes over SSH (machine.go
\* Remote.call) and its reply can be lost.
Remote(m) == m = "kant"

VARIABLES
    live,       \* live[m]: the transcript is where the agent finds it on m
    outgoing,   \* outgoing[m]: parked in ~/.local/state/ferry/parked/<task>-outgoing
    staged,     \* staged[m]: in ~/.local/state/ferry/in/<task>/session
    rec,        \* the task record on kant (record.go Record)
    pc,         \* where the running ferry is in handoff()/finish()
    src, dst,   \* this run's machines
    sess,       \* handoff(): sess != nil
    parked,     \* handoff(): parked
    installed   \* handoff(): installed != nil

vars == <<live, outgoing, staged, rec, pc, src, dst, sess, parked, installed>>

NoMachine == "none"

Init ==
    /\ live = [m \in Machines |-> m = "laptop"]
    /\ outgoing = [m \in Machines |-> FALSE]
    /\ staged = [m \in Machines |-> FALSE]
    \* gen 0 stands for "no record yet"; handoff() then builds one owned by
    \* the source and writes it with Expect 0.
    /\ rec = [owner |-> "laptop", state |-> "owned", from |-> NoMachine,
              to |-> NoMachine, gen |-> 0, hasSource |-> FALSE, hasInstalled |-> FALSE]
    /\ pc = "idle"
    /\ src = NoMachine /\ dst = NoMachine
    /\ sess = FALSE /\ parked = FALSE /\ installed = FALSE

\* Outcomes of one call. "lost" means it did its work but returned an error.
Outcomes(m) == IF ~Faults THEN {"ok"}
               ELSE IF Remote(m) THEN {"ok", "failed", "lost"} ELSE {"ok", "failed"}
Applied(o) == o \in {"ok", "lost"}

Stop == pc' = "idle" /\ src' = NoMachine /\ dst' = NoMachine
        /\ sess' = FALSE /\ parked' = FALSE /\ installed' = FALSE

\* ---- start: main.go `ferry to HOST` / `ferry from HOST NAME` -----------
\* handoff.go: lockTask, then hub.RecordGet. Reading the record can fail.
Start(s) ==
    /\ pc = "idle"
    /\ s = "kant" => rec.gen > 0          \* findTask needs a record
    /\ \/ /\ Faults /\ Stop /\ UNCHANGED <<live, outgoing, staged, rec>>   \* RecordGet fails
       \/ /\ src' = s /\ dst' = Other(s)
          /\ parked' = FALSE
          /\ pc' = CASE rec.state = "landing" /\ rec.owner = Other(s) /\ rec.from = s
                          -> "finishArchive"
                     [] rec.state = "handing-off" /\ rec.from = s -> "unparkFirst"
                     [] OTHER -> "find"
          \* finish() after a rerun uses rec.Source and rec.Installed.
          /\ IF rec.state = "landing" /\ rec.owner = Other(s) /\ rec.from = s
               THEN /\ sess' = rec.hasSource /\ installed' = rec.hasInstalled
               ELSE /\ sess' = FALSE /\ installed' = FALSE
          /\ UNCHANGED <<live, outgoing, staged, rec>>

\* session.go Unpark: move the outgoing copy back, unless something new is
\* in its place. Shared by the start of a rerun and by undo.
UnparkEffect(m) ==
    IF outgoing[m] /\ ~live[m]
      THEN /\ live' = [live EXCEPT ![m] = TRUE]
           /\ outgoing' = [outgoing EXCEPT ![m] = FALSE]
      ELSE UNCHANGED <<live, outgoing>>
UnparkOK(m) == ~(outgoing[m] /\ live[m])

\* handoff.go "An interrupted run may have left the transcript parked".
UnparkFirst ==
    /\ pc = "unparkFirst"
    /\ \E o \in Outcomes(src):
         IF o = "ok" /\ UnparkOK(src)
           THEN UnparkEffect(src) /\ pc' = "find"
                /\ UNCHANGED <<src, dst, sess, parked, installed>>
           ELSE /\ IF Applied(o) /\ UnparkOK(src) THEN UnparkEffect(src) ELSE UNCHANGED <<live, outgoing>>
                /\ Stop
    /\ UNCHANGED <<staged, rec>>

\* src.FindSession: by recorded id or by cwd, it finds the transcript only
\* where the agent would find it.
Find ==
    /\ pc = "find"
    /\ \/ /\ sess' = live[src] /\ pc' = "claim"
          /\ UNCHANGED <<src, dst, parked, installed>>
       \/ Faults /\ Remote(src) /\ Stop     \* the call fails
    /\ UNCHANGED <<live, outgoing, staged, rec>>

\* --- claim --- in handoff.go
Claim ==
    /\ pc = "claim"
    /\ LET resuming == rec.state = "handing-off" /\ rec.from = src /\ rec.to = dst
           refused  == ~resuming /\ (rec.state \in {"handing-off", "landing"} \/ rec.owner # src)
       IN  IF refused
             THEN Stop /\ UNCHANGED rec
             ELSE IF resuming
               THEN pc' = "work" /\ UNCHANGED <<rec, src, dst, sess, parked, installed>>
               ELSE \E o \in Outcomes("kant"):        \* hub.RecordPut, Expect = rec.gen
                      /\ IF Applied(o)
                           THEN rec' = [rec EXCEPT !.state = "handing-off", !.from = src,
                                          !.to = dst, !.gen = rec.gen + 1,
                                          !.hasSource = sess, !.hasInstalled = FALSE]
                           ELSE UNCHANGED rec
                      /\ IF o = "ok"
                           THEN pc' = "work" /\ UNCHANGED <<src, dst, sess, parked, installed>>
                           ELSE Stop
    /\ UNCHANGED <<live, outgoing, staged>>

\* dehydrate, RepoState, InitRepo, Snapshot, moveRefs, Apply, ListExtras,
\* InstallExtras. Each can fail and return err without undo.
Work ==
    /\ pc = "work"
    /\ \/ /\ pc' = IF sess THEN "park" ELSE "commit"
          /\ UNCHANGED <<src, dst, sess, parked, installed>>
       \/ Faults /\ Stop
    /\ UNCHANGED <<live, outgoing, staged, rec>>

\* src.Park(As: "outgoing"). On error: `return err`, with no undo.
Park ==
    /\ pc = "park"
    /\ \E o \in Outcomes(src):
         /\ IF Applied(o) /\ live[src]
              THEN /\ live' = [live EXCEPT ![src] = FALSE]
                   /\ outgoing' = [outgoing EXCEPT ![src] = TRUE]
              ELSE UNCHANGED <<live, outgoing>>
         /\ IF o = "ok"
              THEN parked' = TRUE /\ pc' = "transfer"
                   /\ UNCHANGED <<src, dst, sess, installed>>
              ELSE Stop
    /\ UNCHANGED <<staged, rec>>

\* transfer(src Export, dst Import) into the staging directory. Export
\* prefers the parked copy. On error: undo.
Transfer ==
    /\ pc = "transfer"
    /\ \/ /\ outgoing[src] \/ live[src]
          /\ staged' = [staged EXCEPT ![dst] = TRUE]
          /\ pc' = "install"
       \/ Faults /\ pc' = "undoDst" /\ UNCHANGED staged
    /\ UNCHANGED <<live, outgoing, rec, src, dst, sess, parked, installed>>

\* dst.InstallSession: staging -> live (a copy already there goes to
\* parked/<task>-replaced-*). On error: undo, with installed still nil.
Install ==
    /\ pc = "install"
    /\ \E o \in Outcomes(dst):
         /\ IF Applied(o) /\ staged[dst]
              THEN /\ live' = [live EXCEPT ![dst] = TRUE]
                   /\ staged' = [staged EXCEPT ![dst] = FALSE]
              ELSE UNCHANGED <<live, staged>>
         /\ installed' = (o = "ok")
         /\ pc' = IF o = "ok" THEN "commit" ELSE "undoDst"
    /\ UNCHANGED <<outgoing, rec, src, dst, sess, parked>>

\* --- commit --- hub.RecordPut(landing, owner dst). This is the commit
\* point. If it errors, RecordGet checks whether it landed anyway; if that
\* read also fails, or shows it didn't land, undo.
Commit ==
    /\ pc = "commit"
    /\ \E o \in Outcomes("kant"), readOK \in BOOLEAN:
         /\ IF Applied(o)
              THEN rec' = [rec EXCEPT !.state = "landing", !.owner = dst,
                             !.gen = rec.gen + 1, !.hasInstalled = installed]
              ELSE UNCHANGED rec
         /\ pc' = IF o = "ok" \/ (o = "lost" /\ readOK) THEN "finishArchive"
                  ELSE IF parked THEN "undoDst" ELSE "idle"
         /\ IF pc' = "idle" THEN Stop ELSE UNCHANGED <<src, dst, sess, parked, installed>>
    /\ UNCHANGED <<live, outgoing, staged>>

\* undo(): park the receiver's copy if this run installed it. A failure
\* there is only printed, and undo goes on to put the source copy back.
UndoDst ==
    /\ pc = "undoDst"
    /\ \E o \in Outcomes(dst):
         IF installed /\ Applied(o)
           THEN live' = [live EXCEPT ![dst] = FALSE]
           ELSE UNCHANGED live
    /\ pc' = "undoSrc"
    /\ UNCHANGED <<outgoing, staged, rec, src, dst, sess, parked, installed>>

\* undo(): src.Unpark, then return the original error.
UndoSrc ==
    /\ pc = "undoSrc"
    /\ \E o \in Outcomes(src):
         IF Applied(o) /\ UnparkOK(src) THEN UnparkEffect(src) ELSE UNCHANGED <<live, outgoing>>
    /\ Stop
    /\ UNCHANGED <<staged, rec>>

\* finish(): src.ArchivePark moves the outgoing copy to parked/<task>-g<gen>.
FinishArchive ==
    /\ pc = "finishArchive"
    /\ \E o \in Outcomes(src):
         /\ IF sess /\ Applied(o)
              THEN outgoing' = [outgoing EXCEPT ![src] = FALSE]
              ELSE UNCHANGED outgoing
         /\ IF ~sess \/ o = "ok"
              THEN pc' = "finishRestore" /\ UNCHANGED <<src, dst, sess, parked, installed>>
              ELSE Stop
    /\ UNCHANGED <<live, staged, rec>>

\* finish(): restore hook, then Launch. A failed restore returns an error
\* and leaves the record landing. A failed launch only prints.
FinishRestore ==
    /\ pc = "finishRestore"
    /\ \/ pc' = "finishPut" /\ UNCHANGED <<src, dst, sess, parked, installed>>
       \/ Faults /\ Stop
    /\ UNCHANGED <<live, outgoing, staged, rec>>

\* finish(): hub.RecordPut(owned).
FinishPut ==
    /\ pc = "finishPut"
    /\ \E o \in Outcomes("kant"):
         IF Applied(o)
           THEN rec' = [rec EXCEPT !.state = "owned", !.gen = rec.gen + 1]
           ELSE UNCHANGED rec
    /\ Stop
    /\ UNCHANGED <<live, outgoing, staged>>

\* The laptop dies, sleeps, or ferry is killed between two steps.
Crash ==
    /\ Faults
    /\ pc # "idle"
    /\ Stop
    /\ UNCHANGED <<live, outgoing, staged, rec>>

Next ==
    \/ \E s \in Machines : Start(s)
    \/ UnparkFirst \/ Find \/ Claim \/ Work \/ Park \/ Transfer \/ Install
    \/ Commit \/ UndoDst \/ UndoSrc \/ FinishArchive \/ FinishRestore \/ FinishPut
    \/ Crash

Spec == Init /\ [][Next]_vars

GenBound == rec.gen <= MaxGen

TypeOK ==
    /\ live \in [Machines -> BOOLEAN]
    /\ outgoing \in [Machines -> BOOLEAN]
    /\ staged \in [Machines -> BOOLEAN]
    /\ rec.owner \in Machines
    /\ rec.state \in {"owned", "handing-off", "landing"}

\* ---- the promises -----------------------------------------------------

\* README/PROPOSAL "Fencing": at no point can both machines resume it.
NoFork == ~(live["laptop"] /\ live["kant"])

\* No run, and no crash, leaves the transcript where ferry can't bring it
\* back: it is live, or parked as outgoing where the next run unparks it.
NoLoss == \E m \in Machines : live[m] \/ outgoing[m]

\* When no ferry is running and the record says the task is settled, the
\* owner is the machine that can resume the session.
OwnerCanResume == (pc = "idle" /\ rec.state = "owned") => live[rec.owner]

=============================================================================
