## ~10 obvious things to do

1. **count-unregistered-nodeclaims-against-limits--accept** (3 issues) — High severity, 100% no workaround, median age 50d, working PR exists. Unregistered NodeClaims bypass limits causing unbounded cost explosion. Ship the fix.

2. **respect-pod-level-disruption-controls--accept** (2 issues) — High severity, race condition evicts do-not-disrupt pods. Partial fix landed in v0.36.1; finish the remaining edge cases before users lose trust in the safety annotation.

3. **enforce-disruption-budgets-accurately--accept** (6 issues) — 83% no workaround, median age 327d. Racing reconcile cycles violate disruption budgets. These are correctness bugs in a core safety mechanism.

4. **sync-cluster-state-before-acting--accept** (6 issues) — 83% no workaround. TOCTOU races delete nodes with actively-binding pods. Classic stale-state bugs with clear fixes.

5. **surface-decision-reasons-to-users--accept** (20 issues) — Medium severity, 85% no workaround, 134 comments. Includes a real bug (lo.Reduce accumulator) plus high-demand observability gaps that generate support load.

6. **accurate-pricing-model--accept** (4 issues) — 75% no workaround. Negative cost metric and heap bloat from stale cache entries are straightforward fixes.

7. **passthrough-bottlerocket-settings--accept** (9 issues) — 67% no workaround, median age 682d. TOML merge order silently overrides user settings; map[string]interface{} passthrough drops valid config. Users have no recourse.

8. **validate-cloud-resources-early--accept** (13 issues) — 54% no workaround, median age 306d. The ARN-vs-role-name confusion exhausts instance profile quotas. Early validation with clear errors prevents a costly operational trap.

9. **improve-helm-chart-and-install--accept** (13 issues) — 46% no workaround, 118 comments. CRD migration failures leaving stale v1beta1 objects block upgrades. The flowcontrol API deprecation is a ticking time bomb.

10. **handle-daemonset-pods-correctly--accept** (6 issues) — Termination controller evaluates drain completion incorrectly for DaemonSet pods, and per-instance-type overhead is miscalculated. Both cause observable misbehavior with clear fixes.

## ~5 you would regret not doing

1. **count-unregistered-nodeclaims-against-limits--accept** — Every day this ships unfixed, any cluster experiencing node registration failures or rapid scale-up can blow through cost limits silently. There is zero workaround. The blast radius is financial. A PR already exists. Waiting compounds cost exposure linearly.

2. **respect-pod-level-disruption-controls--accept** — The do-not-disrupt annotation is the primary safety contract with stateful workload operators. The remaining race condition edge cases erode trust in the entire disruption system. Teams that discover their "protected" pods were evicted stop trusting Karpenter and reach for manual node management, which defeats the project's purpose.

3. **exclude-low-priority-pods-from-provisioning--accept** (2 issues, high severity) — Permanently unschedulable pods vetoing all cluster-wide consolidation is a silent cost multiplier. Clusters accumulate these pods naturally (misconfigured jobs, orphaned deployments). The longer you wait, the more clusters are silently overpaying while operators blame Karpenter for "not consolidating."

4. **enforce-disruption-budgets-accurately--accept** — Budget violations from racing reconcile cycles undermine the one mechanism users have to control blast radius during disruptions. If budgets aren't trustworthy, operators add external guardrails that slow adoption.

5. **sync-cluster-state-before-acting--accept** — TOCTOU races deleting nodes with actively-binding pods cause data loss for stateful workloads. This is the kind of bug that generates "Karpenter killed my database" incident reports. Each occurrence creates lasting organizational resistance to autoscaling.

## Top 3 and why

### 1. count-unregistered-nodeclaims-against-limits--accept
**Why this over others:** This is the highest-severity item with the simplest path to completion (PR exists, 3 issues, well-scoped). It's a silent financial safety violation — limits exist specifically to cap spend, and they don't work during the exact scenarios (registration failures, rapid scale) when they matter most. **What it unblocks:** Trustworthy cost governance. Without this, every NodePool limit is aspirational, not enforced. **Cost of waiting:** Unbounded spend exposure for any cluster hitting registration races. One production incident here generates more organizational damage than dozens of feature gaps.

### 2. respect-pod-level-disruption-controls--accept
**Why this over others:** This is the other high-severity accept item and it touches the core trust contract. The do-not-disrupt annotation is how platform teams promise stateful workload owners their pods won't be evicted. A race condition that breaks this promise — even occasionally — makes the annotation unreliable, which makes Karpenter unreliable for stateful workloads. **What it unblocks:** Confident adoption by teams running databases, ML training jobs, and long-running batch. **Cost of waiting:** Each occurrence is a production incident. The partial fix in v0.36.1 means users believe they're protected and are more likely to be surprised.

### 3. enforce-disruption-budgets-accurately--accept
**Why this over others:** Disruption budgets are the primary blast-radius control. With 83% of issues having no workaround and bugs spanning racing reconcile cycles, incorrect deletion-timestamp counting, and PDB-aware candidate selection, the budget system has multiple paths to violation. This beats surface-decision-reasons (higher issue count but lower severity) and sync-cluster-state (comparable urgency but budgets affect more users simultaneously). **What it unblocks:** Safe adoption of aggressive consolidation policies. Teams that can't trust budgets set them to zero and lose consolidation entirely. **Cost of waiting:** Budget violations during drift or consolidation events hit multiple nodes simultaneously. The failure mode is correlated, not independent — exactly when you need budgets to work.