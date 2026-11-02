--------------------------- MODULE SwarmLiveness ---------------------------
(*
  Liveness of one task: it can never get stuck. Lease numbers grow without bound,
  so they are left out here to keep the state space finite. Swarm.tla covers them.

  The environment is allowed to be hostile: agents fail and timeouts fire whenever
  they like. Fairness only says the swarm's own automatic steps are not ignored
  forever, and that an agent that keeps being able to finish eventually does.
*)
EXTENDS Naturals

CONSTANTS MaxTier

VARIABLES state, tier, fails, checked

vars == <<state, tier, fails, checked>>

Init == state = "draft" /\ tier = 1 /\ fails = 0 /\ checked = FALSE

Fail(s) ==
  /\ state = s
  /\ state' = "failed"
  /\ fails' = IF fails < 2 THEN fails + 1 ELSE fails
  /\ UNCHANGED <<tier, checked>>

Publish == state = "draft" /\ state' = "ready" /\ UNCHANGED <<tier, fails, checked>>
Grant == state = "ready" /\ state' = "claimed" /\ UNCHANGED <<tier, fails, checked>>
Start == state = "claimed" /\ state' = "running" /\ UNCHANGED <<tier, fails, checked>>
ExpireClaim == state = "claimed" /\ state' = "ready" /\ UNCHANGED <<tier, fails, checked>>
Submit ==
  /\ state \in {"claimed", "running"} /\ state' = "review" /\ checked' = FALSE
  /\ UNCHANGED <<tier, fails>>
Bad == Fail("running") \/ Fail("claimed") \/ Fail("merging")
ChecksPass == state = "review" /\ state' = "merging" /\ checked' = TRUE /\ UNCHANGED <<tier, fails>>
ChecksFail == Fail("review")
Merged == state = "merging" /\ state' = "done" /\ UNCHANGED <<tier, fails, checked>>
Recover ==
  /\ state = "failed"
  /\ IF fails < 2
       THEN state' = "ready" /\ UNCHANGED <<tier, fails, checked>>
     ELSE IF tier < MaxTier
       THEN state' = "ready" /\ tier' = tier + 1 /\ fails' = 0 /\ UNCHANGED checked
       ELSE state' = "parked" /\ UNCHANGED <<tier, fails, checked>>

Next ==
  \/ Publish \/ Grant \/ Start \/ ExpireClaim \/ Submit \/ Bad
  \/ ChecksPass \/ ChecksFail \/ Merged \/ Recover

\* Automatic steps can never be dropped forever. Grant and ExpireClaim form a
\* cycle with no penalty, so finishing needs strong fairness on Submit and on a
\* passing check: an agent that gets endless chances eventually finishes.
Fairness ==
  /\ WF_vars(Publish) /\ WF_vars(Recover) /\ WF_vars(Merged)
  /\ SF_vars(Grant) /\ SF_vars(Submit) /\ SF_vars(ChecksPass)

Spec == Init /\ [][Next]_vars /\ Fairness

\* Every task ends done, or parked waiting for a human. Never lost, never looping.
EventuallySettles == <>[](state \in {"done", "parked"})

\* Failed is always temporary.
FailedRecovers == [](state = "failed" => <>(state /= "failed"))

=============================================================================
