# Plan

Classify every open issue and produce a work plan. Three phases: classify
issues, then iteratively assign them to actions and refine the actions
(EM-style).

Issues only. PR planning deferred.

## Prerequisites

`data/extracted.jsonl`, `data/fix-themes.jsonl`, `data/evaluated.jsonl`,
and `data/issues.jsonl` must exist from a prior full run.

The team review perspective (anonymized as Mr. Red, Mr. Blue, Mr. Green,
Mr. Gold) is embedded in the classification system prompt. No real names.

## Inputs

| File | Source |
|------|--------|
| `data/issues.jsonl` | download step |
| `data/extracted.jsonl` | extract step |
| `data/fix-themes.jsonl` | aggregate step |
| `data/evaluated.jsonl` | evaluate step |

## Behavior

### Phase 1: Classify (Sonnet, parallel)

For each issue, send: the raw issue JSON (body, comments, links), the
extraction, and the evaluation verdict. The raw issue is included because
the extraction discards cross-references (links to RFCs, KEPs, related
issues) that are needed to classify `has_rfc` correctly.

The system prompt encodes the team review perspective as shared values.
One consensus position per issue. No individual opinions.

Each call returns a classification.

**kind** -- what type of thing is this:

| Kind | Signal |
|------|--------|
| `bug_fix` | Clear behavioral bug with reproduction path |
| `small_change` | Minor fix, docs, config |
| `needs_rfc` | Behavioral change to a core subsystem, no RFC exists |
| `has_rfc` | Issue links to, references, or is an RFC/KEP/design doc |
| `wont_do` | Wrong layer, scope creep, fails earned-complexity test |

**action** -- what happens next:

| Action | When |
|--------|------|
| `accept` | Do it. Bug fixes, small changes, implementations of accepted RFCs. |
| `reject` | Close it. Wrong layer, wont_do, duplicate. |
| `assign_aws` | Needs an AWS employee. Specify expertise in `assignee_hint`. |
| `needs_info` | Ask a specific question in `question` field. |
| `defer` | Not now. Reason in `defer_reason` (e.g., "defer until RFC"). |

Note: `rework` is reserved for future PR planning. For issues, the model
uses `accept` (with guidance in reasoning) or `defer` instead.

**priority**: p0 (safety violation), p1 (cost/perf pain, many users),
p2 (real but workaround exists), p3 (nice to have).

**effort**: trivial, small, medium, large.

### Phase 2: Action Plan (EM-style iteration)

Three sub-steps that iterate until stable.

#### Seed

Code assembles draft actions from themes + classifications. For each
theme in fix-themes.jsonl, group classified issues by action type
(accept, defer, reject, etc). Each group becomes a draft action:

```json
{"action_id": "cost-benefit-consolidation--accept", "action": "accept", "priority": "p1", "effort": "medium", "description": "Evaluate whether each consolidation move is worth executing", "issues": [1234, 1235], "issue_count": 2}
```

The seed is deterministic code, no LLM call. It produces the initial
action drafts that the EM loop refines.

#### E-step: Assign (Sonnet, parallel)

For each classified issue, send: the classification, the extraction, and
a compact list of current action drafts (IDs + descriptions only). The
model assigns the issue to 0-2 actions.

Same pattern as `runAssignIssues` in the aggregate step. Same cwnd
controller. Issues not assigned to any action are flagged as orphaned.

#### M-step: Refine (Opus, single call)

Opus receives: all action drafts with their assigned issue counts, sample
reasonings (2-3 per action), and orphaned issue summaries. Input is
compact -- action summaries, not all issues.

Opus refines the action list:
- Merge actions that cover the same work (even across themes)
- Split actions that are too broad (diverse issue types in one bucket)
- Create new actions for orphaned issues that form a pattern
- Adjust descriptions, priorities, effort estimates
- Drop empty actions (no assignments)

Output: refined action drafts as JSONL.

#### Convergence

Iterate E-step + M-step up to 3 rounds. Stop early if assignment is
stable (< 5% of issues change action between rounds).

### Summary

After convergence, compute and write plan-summary.json from the final
classifications and action assignments.

## Output

### `data/plan-events.jsonl`

Log-structured. Every line is an immutable event. Current state per issue
is the last event for that issue number. The pipeline writes `classified`
events only. Other event types (`action_updated`, `timeout`) are reserved
for future use and not produced by this step.

```json
{"ts": "2026-04-04T14:30:00Z", "repo": "org/repo", "number": 1234, "event": "classified", "kind": "bug_fix", "action": "assign_aws", "priority": "p1", "reasoning": "...", "theme_ids": ["cost-benefit-consolidation"], "effort": "small", "assignee_hint": "scheduling"}
```

### `data/action-plan.jsonl`

One action per line, sorted by priority then issue count descending.

```json
{"action_id": "cost-benefit-consolidation--accept", "action": "assign_aws", "assignee_hint": "disruption/scheduling", "effort": "large", "priority": "p0", "issues": [1234, 1235, 1240], "issue_count": 3, "description": "Implement pre-execution cost-benefit check in consolidation path.", "depends_on": []}
```

### `data/plan-summary.json`

Counts by kind, action, priority, plus EM iteration stats.

```json
{
  "total_issues": 567,
  "by_kind": {"bug_fix": 135, "small_change": 202, "needs_rfc": 210, "has_rfc": 6, "wont_do": 14},
  "by_action": {"accept": 278, "reject": 26, "assign_aws": 4, "needs_info": 29, "defer": 226},
  "by_priority": {"p0": 1, "p1": 42, "p2": 260, "p3": 264},
  "action_plan_count": 106,
  "em_iterations": 3,
  "orphaned_issues": 0
}
```

## CLI

```
./vibish-triage plan --config examples/karpenter.yaml
```

Uses the same `--data-dir`, `--timeout` flags as `run`.

## Cost and Timing

Measured on Karpenter (567 open issues, April 2026):

| Phase | Model | Calls | Time | Cost |
|-------|-------|-------|------|------|
| Classify | Sonnet | 567 | 91s | $9.48 |
| Seed | code | 0 | instant | $0 |
| Assign (round 1) | Sonnet | 567 | 55s | $7.90 |
| Refine (round 1) | Opus | 1 | 3.5 min | $0.46 |
| Assign (round 2) | Sonnet | 567 | 50s | $14.34 |
| Refine (round 2) | Opus | 1 | 4 min | $0.51 |
| Assign (round 3) | Sonnet | 567 | 44s | $15.88 |
| Refine (round 3) | Opus | 1 | 4.5 min | $0.52 |
| **Total (3 rounds)** | | | **~16 min** | **~$49** |

Assign cost increases each round because the action context grows as
Opus refines descriptions. Classify dominates wall-clock time on the
first run; subsequent runs could skip classify with a `--skip-classify`
flag (not yet implemented).

## Validation

### Structural
- Each line in `plan-events.jsonl` is valid JSON with fields: ts, repo, number, event, kind, action, priority, effort.
- `kind` in {bug_fix, small_change, needs_rfc, has_rfc, wont_do}.
- `action` in {accept, reject, assign_aws, rework, needs_info, defer}.
- `priority` in {p0, p1, p2, p3}.
- `effort` in {trivial, small, medium, large}.
- Line count matches `issues.jsonl`.
- `action-plan.jsonl` exists with at least 10 actions.
- Every issue in `action-plan.jsonl` appears in `plan-events.jsonl`.
- `plan-summary.json` exists.
- No real reviewer names in any output.

### Distribution
- `wont_do` is less than 40% of issues.
- `p0` is less than 10% of issues.
- `needs_info` is less than 15% of issues.
- Every kind has at least 1 issue.
- Orphaned issues (not in any action) less than 10%.
