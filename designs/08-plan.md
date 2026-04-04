# Plan

Classify every open issue and produce a work plan. Two passes: parallel
Sonnet classifies each issue, then one Opus call consolidates into actions.

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

### Pass 1: Classify (Sonnet, parallel)

For each issue, send: the extraction, the evaluation verdict (themes +
verdicts from evaluated.jsonl), and the issue title/number. The full issue
JSON is not re-sent -- the extraction already distilled it.

The system prompt encodes the team review perspective as shared values.
One consensus position per issue. No individual opinions.

Each call returns a classification.

**kind** -- what type of thing is this:

| Kind | Signal |
|------|--------|
| `bug_fix` | Clear behavioral bug with reproduction path |
| `small_change` | Minor fix, docs, config |
| `needs_rfc` | Behavioral change to a core subsystem, no RFC exists |
| `has_rfc` | Issue references or is an RFC |
| `wont_do` | Wrong layer, scope creep, fails earned-complexity test |

**action** -- what happens next:

| Action | When |
|--------|------|
| `accept` | Do it. Bug fixes, small changes, implementations of accepted RFCs. |
| `reject` | Close it. Wrong layer, wont_do, duplicate. |
| `assign_aws` | Needs an AWS employee. Specify expertise in `assignee_hint`. |
| `rework` | Send back with guidance in `rework_guidance`. |
| `needs_info` | Ask a specific question in `question` field. |
| `defer` | Not now. Reason in `defer_reason` (e.g., "defer until RFC"). |

**priority**: p0 (safety violation), p1 (cost/perf pain, many users),
p2 (real but workaround exists), p3 (nice to have).

**effort**: trivial, small, medium, large.

### Pass 2: Action Plan (Opus, single call)

Reads all classifications plus fix-themes.jsonl for theme context. Does
not re-derive clusters. Produces one action per line, each covering 1:N
issues.

Merge rules: issues sharing a theme become one action when the fix is
the same work. Split when fixes are distinct even if the theme is shared.
Reject and defer actions can batch multiple issues.

## Output

### `data/plan-events.jsonl`

Log-structured. Every line is an immutable event. Current state per issue
is the last event for that issue number. The pipeline writes `classified`
events only. Other event types (`action_updated`, `timeout`) are reserved
for future use and not produced by this step.

```json
{"ts": "2026-04-03T14:30:00Z", "repo": "org/repo", "number": 1234, "event": "classified", "kind": "bug_fix", "action": "assign_aws", "priority": "p1", "reasoning": "...", "theme_ids": ["cost-benefit-consolidation"], "effort": "small", "assignee_hint": "scheduling"}
```

### `data/action-plan.jsonl`

One action per line, sorted by priority then issue count descending.

```json
{"action_id": "consolidation-cost-benefit", "action": "assign_aws", "assignee_hint": "disruption/scheduling", "effort": "large", "priority": "p0", "issues": [1234, 1235, 1240], "issue_count": 3, "description": "Implement pre-execution cost-benefit check in consolidation path.", "depends_on": []}
```

### `data/plan-summary.json`

Counts by kind, action, and priority. Also includes distribution checks:

```json
{
  "total_issues": 564,
  "by_kind": {"bug_fix": 89, "small_change": 42, "needs_rfc": 65, "has_rfc": 12, "wont_do": 120},
  "by_action": {"accept": 95, "reject": 130, "assign_aws": 85, "rework": 40, "needs_info": 36, "defer": 78},
  "by_priority": {"p0": 15, "p1": 80, "p2": 200, "p3": 169},
  "action_plan_count": 45
}
```

## CLI

```
./vibish-triage plan --config examples/karpenter.yaml
```

Uses the same `--data-dir`, `--timeout` flags as `run`.

## Cost

| Pass | Model | Calls | Est. Cost |
|------|-------|-------|-----------|
| Classify | Sonnet | ~564 | ~$8-10 |
| Action plan | Opus | 1 | ~$0.50 |
| **Total** | | | **~$10** |

## Validation

### Structural
- Each line in `plan-events.jsonl` is valid JSON with fields: ts, repo, number, event, kind, action, priority.
- `kind` in {bug_fix, small_change, needs_rfc, has_rfc, wont_do}.
- `action` in {accept, reject, assign_aws, rework, needs_info, defer}.
- `priority` in {p0, p1, p2, p3}.
- Line count matches `issues.jsonl`.
- `action-plan.jsonl` exists with at least 10 actions.
- Every issue in `action-plan.jsonl` appears in `plan-events.jsonl`.
- `plan-summary.json` exists.
- No real reviewer names in any output.

### Distribution
- `wont_do` is less than 40% of issues. A higher rate suggests the model is rejecting too aggressively.
- `p0` is less than 10% of issues. Safety violations should be rare.
- `needs_info` is less than 15% of issues. The pipeline already has extractions and evaluations -- most issues should be classifiable.
- Every kind has at least 1 issue. A missing kind suggests the model collapsed categories.
