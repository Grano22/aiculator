------------------------------- MODULE Planner -------------------------------
(*
  The planner signal: can an agent be told to exit and then have work appear
  that it should have waited for?

  An exit is only a promise about the current planning epoch. A later epoch is
  a new run, and drivers are started again for it.

  EnforceHints FALSE models the planner being free to ignore its own hints.
*)
EXTENDS Naturals, FiniteSets

CONSTANTS Tiers, MaxEpoch, MaxTasks, EnforceHints

VARIABLES active, epoch, hints, tasks, exited, plannerAlive, nextId

vars == <<active, epoch, hints, tasks, exited, plannerAlive, nextId>>

Unset == 2
NoHints == [t \in Tiers |-> Unset]

\* tasks holds [id, tier, live, epoch]. exited holds [tier, epoch, mark], where
\* mark is the first task id created after the exit.
Init ==
  /\ active = FALSE
  /\ epoch = 0
  /\ hints = NoHints
  /\ tasks = {}
  /\ exited = {}
  /\ plannerAlive = FALSE
  /\ nextId = 0

\* Mirrors Engine.reach: live tasks at or below this tier can reach it.
Reach(tier) == {x \in tasks : x.live /\ x.tier <= tier}

HintsSet == \A t \in Tiers : hints[t] /= Unset

\* Tasks expected at or below a tier, directly or by later escalating to it.
ExpectedUpTo(tier) ==
  LET lower == {t \in Tiers : t <= tier}
  IN \E t \in lower : hints[t] = 1

Begin ==
  /\ ~active /\ epoch < MaxEpoch
  /\ active' = TRUE /\ epoch' = epoch + 1 /\ hints' = NoHints /\ plannerAlive' = TRUE
  /\ UNCHANGED <<tasks, exited, nextId>>

\* A hint is 0 (no tasks at this tier) or 1 (some). Once the sum up to a tier is
\* zero, agents there may already have left, so it may not rise again.
Update(h) ==
  /\ active /\ plannerAlive
  /\ EnforceHints => (HintsSet => \A t \in Tiers : (~ExpectedUpTo(t)) =>
        ~(\E s \in Tiers : s <= t /\ h[s] = 1))
  /\ hints' = h
  /\ UNCHANGED <<active, epoch, tasks, exited, plannerAlive, nextId>>

Add(tier) ==
  /\ active /\ plannerAlive /\ nextId < MaxTasks
  /\ EnforceHints => (HintsSet => ExpectedUpTo(tier))
  /\ tasks' = tasks \cup {[id |-> nextId, tier |-> tier, live |-> TRUE, epoch |-> epoch]}
  /\ nextId' = nextId + 1
  /\ UNCHANGED <<active, epoch, hints, exited, plannerAlive>>

Commit ==
  /\ active /\ plannerAlive
  /\ active' = FALSE /\ plannerAlive' = FALSE /\ hints' = NoHints
  /\ UNCHANGED <<epoch, tasks, exited, nextId>>

PlannerCrash == plannerAlive /\ plannerAlive' = FALSE
  /\ UNCHANGED <<active, epoch, hints, tasks, exited, nextId>>

\* The lease expires: the epoch rises and the abandoned drafts are canceled.
LeaseExpires ==
  /\ active /\ ~plannerAlive /\ epoch < MaxEpoch
  /\ active' = FALSE /\ epoch' = epoch + 1 /\ hints' = NoHints
  /\ tasks' = {IF x.epoch = epoch THEN [x EXCEPT !.live = FALSE] ELSE x : x \in tasks}
  /\ UNCHANGED <<exited, plannerAlive, nextId>>

Finish(x) ==
  /\ x \in tasks /\ x.live
  /\ tasks' = (tasks \ {x}) \cup {[x EXCEPT !.live = FALSE]}
  /\ UNCHANGED <<active, epoch, hints, exited, plannerAlive, nextId>>

Escalate(x) ==
  /\ x \in tasks /\ x.live /\ \E t \in Tiers : t = x.tier + 1
  /\ tasks' = (tasks \ {x}) \cup {[x EXCEPT !.tier = @ + 1]}
  /\ UNCHANGED <<active, epoch, hints, exited, plannerAlive, nextId>>

\* Mirrors Engine.idleResponse: during planning, exit only on a hint of no work;
\* otherwise only when nothing live can reach this tier.
CanExit(tier) ==
  /\ Reach(tier) = {}
  /\ active => (HintsSet /\ ~ExpectedUpTo(tier))

Exit(tier) ==
  /\ CanExit(tier)
  /\ exited' = exited \cup {[tier |-> tier, epoch |-> epoch, mark |-> nextId]}
  /\ UNCHANGED <<active, epoch, hints, tasks, plannerAlive, nextId>>

Next ==
  \/ Begin \/ Commit \/ PlannerCrash \/ LeaseExpires
  \/ \E h \in [Tiers -> {0, 1}] : Update(h)
  \/ \E t \in Tiers : Add(t) \/ Exit(t)
  \/ \E x \in tasks : Finish(x) \/ Escalate(x)

Spec == Init /\ [][Next]_vars

\* An exit holds for its epoch: no task created afterwards in that epoch can be
\* one that would have reached the agent that left.
NoExitBeforeWork ==
  \A e \in exited, x \in tasks :
    ~(x.epoch = e.epoch /\ x.id >= e.mark /\ x.tier <= e.tier)

=============================================================================
