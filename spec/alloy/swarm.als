// Structural rules of the swarm: who may take what, whether plans can get stuck,
// and whether file leases do their job. Each pred mirrors a function named beside it.
// Commands starting with "Bug" break a rule on purpose and must find a counterexample;
// commands starting with "Sane" must find an instance, proving the model is not empty.
module swarm

open util/ordering[Tier]

sig Tier {}
sig Tag {}

sig Agent {
  tier: one Tier,
  tags: set Tag
}

sig Path {
  parent: lone Path
}
fact PathsAreATree { no p: Path | p in p.^parent }

sig Task {
  need: one Tier,
  tagsNeeded: set Tag,
  deps: set Task,
  touches: set Path,
  submitter: lone Agent
}

// Engine.Config.Steal holds any pairs at all, including nonsense ones.
one sig Config { steal: Tier -> Tier }
// Tasks that have waited long enough for their steal rule.
sig Waited in Task {}

// dispatch.go: stealAllowed
pred stealAllowed[a: Agent, t: Task] {
  (a.tier -> t.need) in Config.steal and t in Waited
}

// dispatch.go: eligible, leaving out file conflicts
pred eligible[a: Agent, t: Task] {
  a.tier in t.need + t.need.nexts
  a.tier != t.need implies stealAllowed[a, t]
  t.tagsNeeded in a.tags
}

// The same rule without the first line.
pred eligibleNoFloor[a: Agent, t: Task] {
  a.tier != t.need implies stealAllowed[a, t]
  t.tagsNeeded in a.tags
}

assert NeverBelowItsTier {
  all a: Agent, t: Task | eligible[a, t] implies a.tier in t.need + t.need.nexts
}
check NeverBelowItsTier for 5

assert AlwaysHasRequiredTags {
  all a: Agent, t: Task | eligible[a, t] implies t.tagsNeeded in a.tags
}
check AlwaysHasRequiredTags for 5

assert NoStealWithoutRuleAndWait {
  all a: Agent, t: Task | (eligible[a, t] and a.tier != t.need)
    implies ((a.tier -> t.need) in Config.steal and t in Waited)
}
check NoStealWithoutRuleAndWait for 5

// Without the floor, a nonsense steal rule lets a low tier take a hard task.
assert BugNoFloor {
  all a: Agent, t: Task | eligibleNoFloor[a, t] implies a.tier in t.need + t.need.nexts
}
check BugNoFloor for 5

// dispatch.go: matchReview
pred mayReview[a: Agent, t: Task] {
  a not in t.submitter
  a.tier in t.need + t.need.nexts
}

assert NobodyReviewsOwnWork {
  all a: Agent, t: Task | mayReview[a, t] implies a not in t.submitter
}
check NobodyReviewsOwnWork for 5

// dispatch.go: pathsOverlap
pred overlap[a, b: Path] {
  a = b or a in b.^parent or b in a.^parent
}

assert OverlapIsSymmetric {
  all a, b: Path | overlap[a, b] iff overlap[b, a]
}
check OverlapIsSymmetric for 6

assert FileConflictsWithItsDirectory {
  all f: Path | some f.parent implies overlap[f, f.parent]
}
check FileConflictsWithItsDirectory for 6

assert SiblingsDoNotConflict {
  all a, b: Path | (a != b and a.parent = b.parent and some a.parent) implies not overlap[a, b]
}
check SiblingsDoNotConflict for 6

// Overlap is not transitive, so a check must compare against every active task.
// Comparing only against one representative would miss a conflict.
assert BugOverlapIsTransitive {
  all a, b, c: Path | (overlap[a, b] and overlap[b, c]) implies overlap[a, c]
}
check BugOverlapIsTransitive for 6

pred conflict[t, u: Task] {
  some p: t.touches, q: u.touches | overlap[p, q]
}

// dispatch.go: checkPlanGraph
pred commitAccepted[plan: set Task] {
  no t: plan | t in t.^deps
}

// Any part of an accepted plan: whichever tasks remain, one of them can run.
sig Remaining in Task {}
sig Plan in Task {}

assert AcceptedPlanNeverDeadlocks {
  (commitAccepted[Plan] and some Remaining and Remaining in Plan)
    implies some t: Remaining | no t.deps & Remaining
}
check AcceptedPlanNeverDeadlocks for 6

// Without the cycle check, a plan can leave every remaining task waiting forever.
assert BugNoCycleCheck {
  (some Remaining and Remaining in Plan) implies some t: Remaining | no t.deps & Remaining
}
check BugNoCycleCheck for 6

// File leases over time. dispatch.go: touchConflict
var sig Active in Task {}

pred grant[t: Task] {
  t not in Active
  no u: Active | u != t and conflict[t, u]
  Active' = Active + t
}
pred grantUnchecked[t: Task] {
  t not in Active
  Active' = Active + t
}
pred finish[t: Task] {
  t in Active
  Active' = Active - t
}
pred idle { Active' = Active }

pred activeTasksDoNotConflict {
  no disj t, u: Active | conflict[t, u]
}

assert LeasesKeepWorkApart {
  (no Active and always (idle or some t: Task | grant[t] or finish[t]))
    implies always activeTasksDoNotConflict
}
check LeasesKeepWorkApart for 5 but 12 steps

assert BugLeasesUnchecked {
  (no Active and always (idle or some t: Task | grantUnchecked[t] or finish[t]))
    implies always activeTasksDoNotConflict
}
check BugLeasesUnchecked for 5 but 12 steps

run SaneStealHappens { some a: Agent, t: Task | eligible[a, t] and a.tier != t.need } for 4
run SaneReviewHappens { some a: Agent, t: Task | mayReview[a, t] and some t.submitter } for 4
run SanePlanRuns { commitAccepted[Plan] and #Plan > 1 and some Plan.deps } for 4
