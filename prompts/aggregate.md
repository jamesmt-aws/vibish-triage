# Aggregate: Identify Fix Themes from Issue Diagnoses

You will receive {{.IssueCount}} issue extractions, each with a what_went_wrong
diagnosis and 1-3 proposed fixes. Your job is to find the underlying fix themes:
groups of issues that need the same behavioral change.

## How to Name Themes

Name each theme by the **behavioral decision that should change**, not by the
feature area or mechanism affected. The right level of abstraction is: "what
question should the system ask before acting?" not "what specific mechanism is
broken?"

Good theme names:
- "Evaluate disruption cost before consolidating" (decision: should this move happen?)
- "Count unregistered NodeClaims against limits" (decision: is this node counted?)
- "Surface blocking reasons as status conditions" (decision: is the user told why?)
- "Batch drift replacements instead of replacing one at a time" (decision: how many at once?)

Bad theme names (too granular — these are sub-mechanisms, not decisions):
- "Fix multi-node consolidation candidate selection" (mechanism within cost-benefit)
- "Parallelize consolidation evaluation" (implementation detail of consolidation)
- "Emit pod-level metrics for disruptions" (one channel within observability)
- "Fix metric bookkeeping for negative gauges" (specific bug within metrics)
- "Eagerly sync disruption taint to cluster state" (one race condition instance)

Bad theme names (too vague — categories, not actions):
- "Improve consolidation" (feature area, not a decision)
- "Fix NodePool limits" (too vague — what decision changes?)
- "Observability improvements" (category, not an action)

## Level of Abstraction Test

Before finalizing each theme, ask: "Would a previous version of this theme
have been split into these sub-themes, or would they all be one theme?" If
multiple themes all describe different failure modes of the **same behavioral
decision**, merge them into one theme named after the decision.

Examples of decisions that should be ONE theme each:
- All issues where consolidation makes a move that isn't worth it → one theme
  (covers: minimum savings threshold, multi-node candidate selection,
  kube-scheduler divergence causing churn, premature consolidation of new
  nodes, simulation inaccuracy leading to bad moves). The unifying question
  is "should this consolidation move happen?" — every sub-problem is a
  different way that question gets answered wrong.
- All issues about users not knowing what happened or why → one theme
  (covers: missing events, missing metrics, missing status conditions,
  missing log context, missing tracing)
- All issues about race conditions between controllers → one theme
  (covers: informer lag, stale cluster state, TOCTOU on emptiness check,
  disruption budget enforcement timing)
- All issues about DaemonSet handling → one theme
  (covers: overhead calculation, custom controllers, drain behavior)

## How to Cluster

Group issues that would be resolved by the **same code change**, even if the
root causes differ. "Consolidation ignores restart cost," "kube-scheduler
places pods differently than simulated," and "multi-node consolidation picks
bad candidates" all have different root causes but the same fix: better
evaluation before executing consolidation moves. That is one theme.

An issue can map to multiple themes.

If a group of issues are all feature requests for configuration surface area
(e.g., "expose field X", "add option Y"), that is one theme: "Expand
configuration surface for [subsystem]". Do not create one theme per field.

Aim for 40-55 themes. Merge aggressively. Err on the side of fewer, broader
themes rather than more, narrower ones.

## Theme Type

Classify each theme as one of:
- **behavioral_change**: The system should make a different decision. These are
  design bugs or missing decision logic.
- **feature_surface**: Users want more configuration knobs, fields, or options.
  These are wish lists, not behavioral changes.
- **infrastructure**: CI, testing, release automation, documentation.

## Severity

Classify each theme's typical severity based on the worst-case impact of the
issues it contains:
- **high**: Causes downtime, data loss, cost explosion, or violates safety
  guarantees (e.g., disruption budget violations, provisioning past limits).
- **medium**: Degraded performance, suboptimal decisions, workaround exists.
- **low**: Cosmetic, documentation, minor inconvenience.
{{if .DomainContext}}

## Domain Context

{{.DomainContext}}
{{end}}
{{if .KnownFixes}}

## Effort Calibration

These fixes are already understood. Use them as anchors for effort estimates.
{{range .KnownFixes}}
> **{{.Title}}** (effort: {{.Effort}})
> {{.Rationale}}
{{end}}

A theme is **low** effort if it changes decision logic within existing code
paths — a single check, a threshold, a condition. No new APIs, no schema
changes. Most behavioral-decision themes that fix an existing decision are low.

A theme is **medium** only if it requires new logic across multiple components,
new API fields, or new controllers.

A theme is **high** only if it requires architectural rework or a new subsystem.

Err on the side of low. If in doubt between low and medium, choose low.
{{end}}

## Output Schema

For each theme, return:
- theme_id (kebab-case)
- title (imperative sentence)
- description (1-2 sentences)
- theme_type (behavioral_change / feature_surface / infrastructure)
- severity (high / medium / low)
- effort_estimate (low / medium / high)
- effort_rationale (1 sentence)
