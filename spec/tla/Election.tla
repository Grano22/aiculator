---------------------------- MODULE Election ----------------------------
(* Which server writes to a data directory, as built in cmd/aiculator/leader.go.

   Servers run on one machine and share one data directory. A server is the leader when
   it holds the kernel lock on that directory. The others stand by and take the lock
   when it comes free. The kernel frees the lock only when the holder's process is gone.

   A process can also pause, from a stopped terminal, a debugger, swap or a slow disk.
   While paused it neither writes nor answers health checks, but it is not dead, and on
   waking it carries on from where it was, still believing it leads.

   AtMostOneWriter  No two servers that believe they lead are running at once.
   LeaderHasLock    Whoever believes it leads holds the lock.
   UniqueTerms      No two leaders, past or present, have shared a term.
   Recovers         If a server is ready to stand by, some server eventually leads.

   Bug = "health" elects on a missed health check instead of the lock: a standby takes
   over when the leader stops answering. A paused leader stops answering too, and when
   it wakes there are two writers. That is why the lock decides. *)
EXTENDS Naturals, FiniteSets, TLC

CONSTANTS Servers, Bug, MaxTerm

None == "none"
Roles == {"standby", "leader", "down"}

VARIABLES lock, role, alive, paused, term, termOf
vars == <<lock, role, alive, paused, term, termOf>>

TypeOK ==
  /\ lock \in Servers \cup {None}
  /\ role \in [Servers -> Roles]
  /\ alive \in [Servers -> BOOLEAN]
  /\ paused \in [Servers -> BOOLEAN]
  /\ term \in 0..MaxTerm
  /\ termOf \in [Servers -> 0..MaxTerm]

Init ==
  /\ lock = None
  /\ role = [s \in Servers |-> "standby"]
  /\ alive = [s \in Servers |-> TRUE]
  /\ paused = [s \in Servers |-> FALSE]
  /\ term = 0
  /\ termOf = [s \in Servers |-> 0]

Running(s) == alive[s] /\ ~paused[s]

\* What lets a standby take over. The lock is free only once its holder has exited; a
\* health check fails as soon as the leader stops answering, for whatever reason.
MayLead(s) ==
  IF Bug = "health"
  THEN \A o \in Servers \ {s} : role[o] = "leader" => ~Running(o)
  ELSE lock = None

\* The term saturates so that the model stays finite; UniqueTerms is checked below it.
Lead(s) ==
  /\ role[s] = "standby" /\ Running(s) /\ MayLead(s)
  /\ lock' = s
  /\ role' = [role EXCEPT ![s] = "leader"]
  /\ term' = IF term < MaxTerm THEN term + 1 ELSE term
  /\ termOf' = [termOf EXCEPT ![s] = term']
  /\ UNCHANGED <<alive, paused>>

\* Exiting, cleanly or by kill -9. The kernel frees the lock either way.
Exit(s) ==
  /\ alive[s]
  /\ alive' = [alive EXCEPT ![s] = FALSE]
  /\ paused' = [paused EXCEPT ![s] = FALSE]
  /\ role' = [role EXCEPT ![s] = "down"]
  /\ lock' = IF lock = s THEN None ELSE lock
  /\ UNCHANGED <<term, termOf>>

Pause(s) ==
  /\ Running(s)
  /\ paused' = [paused EXCEPT ![s] = TRUE]
  /\ UNCHANGED <<lock, role, alive, term, termOf>>

Resume(s) ==
  /\ alive[s] /\ paused[s]
  /\ paused' = [paused EXCEPT ![s] = FALSE]
  /\ UNCHANGED <<lock, role, alive, term, termOf>>

\* aiculator up --standby, or a supervisor, starting the server again.
Restart(s) ==
  /\ ~alive[s]
  /\ alive' = [alive EXCEPT ![s] = TRUE]
  /\ role' = [role EXCEPT ![s] = "standby"]
  /\ UNCHANGED <<lock, paused, term, termOf>>

Next == \E s \in Servers : Lead(s) \/ Exit(s) \/ Pause(s) \/ Resume(s) \/ Restart(s)

\* A paused process wakes eventually, and a standby that keeps getting the chance takes it.
Fairness == \A s \in Servers : WF_vars(Resume(s)) /\ SF_vars(Lead(s)) /\ WF_vars(Restart(s))

Spec == Init /\ [][Next]_vars /\ Fairness

Leaders == {s \in Servers : role[s] = "leader"}

AtMostOneWriter == Cardinality({s \in Leaders : Running(s)}) <= 1

LeaderHasLock == \A s \in Leaders : lock = s

UniqueTerms == term < MaxTerm =>
  \A s, o \in Leaders : s # o => termOf[s] # termOf[o]

\* Terms only grow, so a takeover is visible to anyone comparing them.
TermsGrow == [][term' >= term]_vars

Recovers == (\E s \in Servers : role[s] = "standby" /\ alive[s]) ~> (Leaders # {})
=============================================================================
