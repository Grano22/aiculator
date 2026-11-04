---------------------------- MODULE Sync ----------------------------
(* Two-way sync between the swarm and another system, for one task, as built in
   internal/connect/sync.go.

   local   is the task's state in the swarm, which only the swarm moves forward.
   remote  is the status on the other system. A person can cancel there.
   link    is the last remote status the swarm saw or set.
   pending is the newest state still to be reported, from the durable commit stream.
   sent    is the state whose push reached the remote, before the link was updated.

   A push can be delivered and then lost to a crash before it is recorded, so delivery
   is at-least-once. The properties below say this is still safe:

   NoEcho        A status the remote already has, as far as the swarm knows, is never sent.
   Settles       Sync finishes: nothing goes back and forth forever.
   Agrees        Once settled, with nobody interfering, the remote says what the swarm says.
   CancelHonored Once the swarm has seen a remote cancel, it stops the task.

   Bug = "noecho" removes the comparison with the link, to prove NoEcho catches that. *)
EXTENDS Naturals, TLC

CONSTANTS Bug, MaxCrashes

States == {"open", "working", "done", "canceled"}
Terminal == {"done", "canceled"}
None == "none"

\* The status each state maps to. An open task has not been reported on.
Status(s) == CASE s = "open"     -> "todo"
               [] s = "working"  -> "inprogress"
               [] s = "done"     -> "finished"
               [] s = "canceled" -> "cancelled"

VARIABLES local, remote, link, pending, sent, crashes, human, echoed, pushes

vars == <<local, remote, link, pending, sent, crashes, human, echoed, pushes>>

TypeOK ==
  /\ local \in States
  /\ remote \in {"todo", "inprogress", "finished", "cancelled"}
  /\ link \in {"todo", "inprogress", "finished", "cancelled"}
  /\ pending \in States \cup {None}
  /\ sent \in States \cup {None}
  /\ crashes \in 0..MaxCrashes
  /\ human \in BOOLEAN
  /\ echoed \in BOOLEAN
  /\ pushes \in 0..(3 * (MaxCrashes + 2))

Init ==
  /\ local = "open"
  /\ remote = "todo"
  /\ link = "todo"
  /\ pending = None
  /\ sent = None
  /\ crashes = 0
  /\ human = FALSE
  /\ echoed = FALSE
  /\ pushes = 0

\* The swarm commits a change. The newest one replaces whatever was waiting.
Commit(next) ==
  /\ local \notin Terminal
  /\ local' = next
  /\ pending' = next
  /\ UNCHANGED <<remote, link, sent, crashes, human, echoed, pushes>>

Work == local = "open" /\ Commit("working")
Finish == local = "working" /\ Commit("done")

\* The commit stream hands over a state, and the push decides what to do with it.
Skip ==
  /\ pending # None
  /\ sent = None
  /\ Bug # "noecho"
  /\ Status(pending) = link
  /\ pending' = None
  /\ UNCHANGED <<local, remote, link, sent, crashes, human, echoed, pushes>>

Push ==
  /\ pending # None
  /\ sent = None
  /\ Bug = "noecho" \/ Status(pending) # link
  /\ pushes < 3 * (MaxCrashes + 2)
  /\ remote' = Status(pending)
  /\ sent' = pending
  /\ pushes' = pushes + 1
  /\ echoed' = (echoed \/ Status(pending) = link)
  /\ UNCHANGED <<local, link, pending, crashes, human>>

\* It records what was sent, not what the remote says by now, so that a cancel made during
\* the call still looks new to the next read. A newer state stays, and is sent next.
Record ==
  /\ sent # None
  /\ link' = Status(sent)
  /\ sent' = None
  /\ pending' = IF pending = sent THEN None ELSE pending
  /\ UNCHANGED <<local, remote, crashes, human, echoed, pushes>>

\* The server dies after the remote call and before it records it. The revision was not
\* acknowledged, so it is delivered again.
Crash ==
  /\ sent # None
  /\ crashes < MaxCrashes
  /\ sent' = None
  /\ crashes' = crashes + 1
  /\ UNCHANGED <<local, remote, link, pending, human, echoed, pushes>>

\* A person cancels on the other system, at most once.
HumanCancel ==
  /\ ~human
  /\ local \notin Terminal
  /\ remote' = "cancelled"
  /\ human' = TRUE
  /\ UNCHANGED <<local, link, pending, sent, crashes, echoed, pushes>>

\* The swarm reads the remote, by polling, a webhook, or before a push. A cancel it has not
\* seen before stops the task, which is itself a commit, and so reaches the stream.
Pull ==
  /\ sent = None
  /\ link # remote
  /\ link' = remote
  /\ IF remote = "cancelled" /\ local \notin Terminal
       THEN local' = "canceled" /\ pending' = "canceled"
       ELSE UNCHANGED <<local, pending>>
  /\ UNCHANGED <<remote, sent, crashes, human, echoed, pushes>>

Next ==
  \/ Work \/ Finish \/ Skip \/ Push \/ Record \/ Crash \/ HumanCancel \/ Pull

Spec == Init /\ [][Next]_vars
        /\ WF_vars(Skip) /\ WF_vars(Push) /\ WF_vars(Record) /\ WF_vars(Pull)

NoEcho == ~echoed

Quiet == pending = None /\ sent = None /\ link = remote

\* Without a person interfering, a quiet system agrees, once a task has been reported on.
Agrees == (Quiet /\ ~human /\ local # "open") => remote = Status(local)

\* The swarm never sits on a cancel it has seen, unless the task had already finished.
CancelHonored == (link = "cancelled" /\ pending = None /\ sent = None) => local \in Terminal

Settles == <>[](pending = None /\ sent = None)

=============================================================================
