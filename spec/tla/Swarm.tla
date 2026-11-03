------------------------------- MODULE Swarm -------------------------------
(*
  Safety of the task lifecycle, including writer crashes, lost acknowledgements
  and zombie agents.

  kv is durable: it is what the CAS write commits. held is what agents believe,
  and it survives a writer crash, which is exactly how zombies arise. Every step
  of a task goes through the Transitions table, and TestSpecMatchesCode in
  internal/core fails if that table and core.Apply ever disagree.

  Bug injects a defect, so that the checker can be shown to catch it:
    "none"        the real design
    "nofence"     submit does not check the lease
    "selfverify"  a submitter may review its own work
*)
EXTENDS Naturals, FiniteSets

CONSTANTS Tasks, Agents, MaxLease, MaxTier, Bug

None == "none"
System == "system"

Transitions == {
  <<"draft", "publish", "ready">>,
  <<"draft", "cancel", "canceled">>,
  <<"ready", "grant", "claimed">>,
  <<"ready", "cancel", "canceled">>,
  <<"claimed", "heartbeat", "running">>,
  <<"claimed", "progress", "running">>,
  <<"claimed", "submit", "review">>,
  <<"claimed", "fail", "failed">>,
  <<"claimed", "release", "ready">>,
  <<"claimed", "expire", "ready">>,
  <<"claimed", "cancel", "canceled">>,
  <<"running", "heartbeat", "running">>,
  <<"running", "progress", "running">>,
  <<"running", "submit", "review">>,
  <<"running", "fail", "failed">>,
  <<"running", "release", "ready">>,
  <<"running", "expire", "failed">>,
  <<"running", "cancel", "canceled">>,
  <<"review", "grant", "review">>,
  <<"review", "heartbeat", "review">>,
  <<"review", "checks", "review">>,
  <<"review", "checks", "merging">>,
  <<"review", "checks", "failed">>,
  <<"review", "verdict", "merging">>,
  <<"review", "verdict", "failed">>,
  <<"review", "fail", "failed">>,
  <<"review", "expire", "review">>,
  <<"review", "cancel", "canceled">>,
  <<"merging", "merged", "done">>,
  <<"merging", "fail", "failed">>,
  <<"merging", "cancel", "canceled">>,
  <<"failed", "recover", "ready">>,
  <<"failed", "recover", "parked">>,
  <<"failed", "cancel", "canceled">>,
  <<"parked", "unpark", "ready">>,
  <<"parked", "cancel", "canceled">>,
  \* Amend changes what a task says and never its state. Only the swarm may send it.
  <<"draft", "amend", "draft">>,
  <<"ready", "amend", "ready">>,
  <<"claimed", "amend", "claimed">>,
  <<"running", "amend", "running">>,
  <<"review", "amend", "review">>,
  <<"merging", "amend", "merging">>,
  <<"done", "amend", "done">>,
  <<"failed", "amend", "failed">>,
  <<"parked", "amend", "parked">>,
  <<"canceled", "amend", "canceled">>
}

States == {"draft", "ready", "claimed", "running", "review", "merging",
           "done", "failed", "parked", "canceled"}

VARIABLES kv, held, grants, up

vars == <<kv, held, grants, up>>

Actors == Agents \cup {None}

TaskRec == [state: States, owner: Actors, lease: 0..MaxLease, submitter: Actors,
            verifier: Actors \cup {System}, tier: 1..MaxTier, fails: 0..2,
            checked: BOOLEAN, needReview: BOOLEAN]

TypeOK ==
  /\ kv \in [Tasks -> TaskRec]
  /\ held \subseteq (Agents \X Tasks \X (1..MaxLease))
  /\ grants \subseteq (Agents \X Tasks \X (1..MaxLease))
  /\ up \in BOOLEAN

Init ==
  /\ kv \in [Tasks -> {[state |-> "draft", owner |-> None, lease |-> 0, submitter |-> None,
                        verifier |-> None, tier |-> 1, fails |-> 0, checked |-> FALSE,
                        needReview |-> r] : r \in BOOLEAN}]
  /\ held = {}
  /\ grants = {}
  /\ up = TRUE

\* Every commit is one guarded write to one key, the CAS the implementation does.
Commit(t, cmd, new, rec) ==
  /\ up
  /\ <<kv[t].state, cmd, new>> \in Transitions
  /\ kv' = [kv EXCEPT ![t] = [rec EXCEPT !.state = new]]

Publish(t) == Commit(t, "publish", "ready", kv[t]) /\ UNCHANGED <<held, grants, up>>

\* The commit happens first and the reply may be lost: see Deliver and Crash.
Grant(a, t) ==
  /\ kv[t].lease < MaxLease
  /\ Commit(t, "grant", "claimed",
       [kv[t] EXCEPT !.owner = a, !.lease = @ + 1])
  /\ grants' = grants \cup {<<a, t, kv[t].lease + 1>>}
  /\ UNCHANGED <<held, up>>

Deliver(a, t, l) ==
  /\ up
  /\ <<a, t, l>> \in grants
  /\ held' = held \cup {<<a, t, l>>}
  /\ UNCHANGED <<kv, grants, up>>

\* The fencing rule from core.Apply: current owner, current lease.
Fenced(a, t, l) ==
  \/ Bug = "nofence"
  \/ kv[t].owner = a /\ kv[t].lease = l

Heartbeat(a, t, l) ==
  /\ <<a, t, l>> \in held
  /\ Fenced(a, t, l)
  /\ kv[t].state \in {"claimed", "running"}
  /\ Commit(t, "heartbeat", "running", kv[t])
  /\ UNCHANGED <<held, grants, up>>

Submit(a, t, l) ==
  /\ <<a, t, l>> \in held
  /\ Fenced(a, t, l)
  /\ Commit(t, "submit", "review",
       [kv[t] EXCEPT !.owner = None, !.submitter = a, !.verifier = None, !.checked = FALSE])
  /\ UNCHANGED <<held, grants, up>>

Failure(rec) == [rec EXCEPT !.owner = None, !.fails = IF @ < 2 THEN @ + 1 ELSE @]

AgentFail(a, t, l) ==
  /\ <<a, t, l>> \in held
  /\ kv[t].state \in {"claimed", "running"}
  /\ Fenced(a, t, l)
  /\ Commit(t, "fail", "failed", Failure(kv[t]))
  /\ UNCHANGED <<held, grants, up>>

Release(a, t, l) ==
  /\ <<a, t, l>> \in held
  /\ Fenced(a, t, l)
  /\ Commit(t, "release", "ready", [kv[t] EXCEPT !.owner = None])
  /\ UNCHANGED <<held, grants, up>>

\* A timeout can fire at any moment. Time itself is abstracted away.
ExpireClaim(t) ==
  /\ kv[t].state = "claimed"
  /\ Commit(t, "expire", "ready", [kv[t] EXCEPT !.owner = None])
  /\ UNCHANGED <<held, grants, up>>

ExpireRunning(t) ==
  /\ kv[t].state = "running"
  /\ Commit(t, "expire", "failed", Failure(kv[t]))
  /\ UNCHANGED <<held, grants, up>>

ExpireReviewer(t) ==
  /\ kv[t].state = "review" /\ kv[t].owner /= None
  /\ Commit(t, "expire", "review", [kv[t] EXCEPT !.owner = None])
  /\ UNCHANGED <<held, grants, up>>

Checks(t, pass) ==
  /\ kv[t].state = "review" /\ ~kv[t].checked
  /\ IF ~pass
       THEN Commit(t, "checks", "failed", Failure([kv[t] EXCEPT !.checked = TRUE]))
     ELSE IF kv[t].needReview
       THEN Commit(t, "checks", "review", [kv[t] EXCEPT !.checked = TRUE])
       ELSE Commit(t, "checks", "merging",
              [kv[t] EXCEPT !.checked = TRUE, !.owner = None, !.verifier = System])
  /\ UNCHANGED <<held, grants, up>>

ReviewGrant(a, t) ==
  /\ kv[t].state = "review" /\ kv[t].owner = None
  /\ kv[t].needReview /\ kv[t].checked
  /\ kv[t].lease < MaxLease
  /\ a /= kv[t].submitter \/ Bug = "selfverify"
  /\ Commit(t, "grant", "review", [kv[t] EXCEPT !.owner = a, !.lease = @ + 1])
  /\ grants' = grants \cup {<<a, t, kv[t].lease + 1>>}
  /\ UNCHANGED <<held, up>>

Verdict(a, t, l, pass) ==
  /\ <<a, t, l>> \in held
  /\ kv[t].state = "review"
  /\ a /= kv[t].submitter \/ Bug = "selfverify"
  /\ Fenced(a, t, l)
  /\ kv[t].owner = a
  /\ IF pass
       THEN /\ kv[t].checked
            /\ Commit(t, "verdict", "merging", [kv[t] EXCEPT !.owner = None, !.verifier = a])
       ELSE Commit(t, "verdict", "failed", Failure(kv[t]))
  /\ UNCHANGED <<held, grants, up>>

Merged(t) == Commit(t, "merged", "done", kv[t]) /\ UNCHANGED <<held, grants, up>>

\* The swarm's own failures: a merge conflict, or checks that could not run.
SystemFail(t) ==
  /\ kv[t].state \in {"review", "merging"}
  /\ Commit(t, "fail", "failed", Failure(kv[t]))
  /\ UNCHANGED <<held, grants, up>>

Recover(t) ==
  /\ kv[t].state = "failed"
  /\ IF kv[t].fails < 2
       THEN Commit(t, "recover", "ready", kv[t])
     ELSE IF kv[t].tier < MaxTier
       THEN Commit(t, "recover", "ready", [kv[t] EXCEPT !.tier = @ + 1, !.fails = 0])
       ELSE Commit(t, "recover", "parked", kv[t])
  /\ UNCHANGED <<held, grants, up>>

Unpark(t) ==
  Commit(t, "unpark", "ready", [kv[t] EXCEPT !.fails = 0]) /\ UNCHANGED <<held, grants, up>>

Cancel(t) ==
  /\ kv[t].state \notin {"done", "canceled"}
  /\ Commit(t, "cancel", "canceled", [kv[t] EXCEPT !.owner = None])
  /\ UNCHANGED <<held, grants, up>>

\* kill -9: unacknowledged replies vanish, but kv and what agents hold survive.
Crash == up /\ up' = FALSE /\ UNCHANGED <<kv, held, grants>>
Restart == ~up /\ up' = TRUE /\ UNCHANGED <<kv, held, grants>>

Next ==
  \/ Crash \/ Restart
  \/ \E t \in Tasks :
       \/ Publish(t) \/ ExpireClaim(t) \/ ExpireRunning(t) \/ ExpireReviewer(t)
       \/ Checks(t, TRUE) \/ Checks(t, FALSE)
       \/ Merged(t) \/ SystemFail(t) \/ Recover(t) \/ Unpark(t) \/ Cancel(t)
  \/ \E a \in Agents, t \in Tasks :
       \/ Grant(a, t) \/ ReviewGrant(a, t)
  \/ \E g \in grants : Deliver(g[1], g[2], g[3])
  \/ \E h \in held :
       \/ Heartbeat(h[1], h[2], h[3])
       \/ Submit(h[1], h[2], h[3])
       \/ AgentFail(h[1], h[2], h[3])
       \/ Release(h[1], h[2], h[3])
       \/ Verdict(h[1], h[2], h[3], TRUE)
       \/ Verdict(h[1], h[2], h[3], FALSE)

Spec == Init /\ [][Next]_vars

---------------------------------------------------------------------------
\* The promises.

\* A lease number is never given to two agents, across any number of crashes.
UniqueLease ==
  \A g1, g2 \in grants : (g1[2] = g2[2] /\ g1[3] = g2[3]) => g1[1] = g2[1]

\* Mirrors core.Validate.
LeaseShape ==
  \A t \in Tasks :
    /\ kv[t].state \in {"claimed", "running"} => kv[t].owner /= None /\ kv[t].lease > 0
    /\ kv[t].state \in {"draft", "ready", "merging", "done", "failed", "parked", "canceled"}
         => kv[t].owner = None

\* Done always means verified by someone other than the author.
DoneMeansIndependentlyVerified ==
  \A t \in Tasks :
    kv[t].state \in {"merging", "done"} =>
      /\ kv[t].verifier /= None
      /\ kv[t].verifier /= kv[t].submitter

\* A task needing review can never skip it.
ReviewCannotBeSkipped ==
  \A t \in Tasks :
    (kv[t].state \in {"merging", "done"} /\ kv[t].needReview) => kv[t].verifier \in Agents

\* Step properties, checked as temporal formulas.
LeaseNeverGoesBack == [][\A t \in Tasks : kv'[t].lease >= kv[t].lease]_vars
DoneIsFinal == [][\A t \in Tasks : kv[t].state = "done" => kv'[t].state = "done"]_vars
TierNeverDrops == [][\A t \in Tasks : kv'[t].tier >= kv[t].tier]_vars

\* Only the agent holding the current lease can move a task out of claimed or
\* running to review; a zombie's old lease can never do it.
ZombieCannotSubmit ==
  [][\A t \in Tasks :
      (kv[t].state \in {"claimed", "running"} /\ kv'[t].state = "review")
        => \E a \in Agents :
             /\ kv'[t].submitter = a
             /\ a = kv[t].owner]_vars

=============================================================================
