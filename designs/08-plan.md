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

**Memoization.** Classify results are cached by a hash of (issue +
extraction + evaluation). On re-run, only issues whose inputs changed
get re-classified. Cache lives in `data/.plan-cache/`. This makes
iterating on the EM parameters cheap — classify runs once, subsequent
plan runs skip it automatically.

**kind** -- what type of thing is this:

| Kind | Signal |
|------|--------|
| `bug_fix` | Clear behavioral bug with reproduction path |
| `small_change` | Obviously correct fix: docs, config, typo, one-line change |
| `needs_rfc` | Behavioral change to a core subsystem, no RFC exists |
| `has_obvious_rfc` | A link to an RFC, KEP, or design doc is visible in the issue body or comments |
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

Issues without theme assignments are grouped by action type into
`unthemed--{action}` buckets. This is expected -- the upstream aggregate
step does not assign every issue to a theme. Unthemed issues still get
assigned to actions during the E-step.

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

Default: 2 rounds (`--max-em-rounds`). Stop early if assignment is
stable (< 5% of issues change action between rounds). A third round
buys ~2% more stability at ~$15 extra cost; usually not worth it.

#### Post-hoc: Breakdown (Haiku, top 10 actions)

After the EM loop, send each of the top 10 actions (by issue count) to
Haiku with the action description and its issues' titles + extractions.
Ask: how many distinct pieces of work are in this action, and how many
are obviously correct fixes (`small_change`) that someone could merge
without debate?

This is diagnostic. The breakdown is appended to the action in
action-plan.jsonl as a `breakdown` field:

```json
{"distinct_updates": 6, "obviously_correct": 42, "needs_discussion": 27, "summary": "3 broken-link batches (trivial), 2 missing guides (medium writing), 1 policy decision (needs team input)"}
```

Cost: ~10 Haiku calls, negligible.

### Summary

After convergence and breakdown, compute and write plan-summary.json
from the final classifications and action assignments.

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

Counts by kind, action, priority, EM iteration stats, and spread metrics.

```json
{
  "total_issues": 567,
  "by_kind": {"bug_fix": 147, "small_change": 180, "needs_rfc": 217, "has_obvious_rfc": 9, "wont_do": 13},
  "by_action": {"accept": 281, "reject": 25, "assign_aws": 3, "needs_info": 27, "defer": 229},
  "by_priority": {"p0": 1, "p1": 43, "p2": 261, "p3": 261},
  "action_plan_count": 89,
  "em_iterations": 2,
  "orphaned_issues": 7,
  "spread": {
    "gini": 0.52,
    "top_5_pct": 34.2,
    "top_10_pct": 51.8,
    "top_20_pct": 72.1,
    "median_action_size": 3,
    "max_action_size": 70
  }
}
```

Spread metrics are diagnostic -- they tell you whether a few actions
dominate the backlog or work is evenly distributed. A high Gini (> 0.6)
means a small number of actions cover most issues; a low Gini (< 0.3)
means actions are roughly equal size. Neither is inherently good or bad.

`top_N_pct` is the percentage of assigned issues covered by the top N
actions by issue count. Useful for answering "if we staff the top 10
actions, what fraction of the backlog do we address?"
```

## CLI

```
./vibish-triage plan --config examples/karpenter.yaml
./vibish-triage plan --config examples/karpenter.yaml --max-em-rounds 3
```

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | `triage.yaml` | Path to config file |
| `--data-dir` | `./data` | Directory for input/output data |
| `--prompts-dir` | `./prompts` | Directory containing prompt templates |
| `--timeout` | `90m` | Max time per LLM phase |
| `--max-em-rounds` | `2` | Maximum EM iterations for action plan |

## Cost and Timing

Measured on Karpenter (567 open issues, April 2026):

| Phase | Model | Calls | Time | Cost |
|-------|-------|-------|------|------|
| Classify (first run) | Sonnet | 567 | 91s | $9.48 |
| Classify (cached) | — | 0 | instant | $0 |
| Seed | code | 0 | instant | $0 |
| Assign (per round) | Sonnet | 567 | ~50s | ~$8-16 |
| Refine (per round) | Opus | 1 | ~4 min | ~$0.50 |
| **Total (2 rounds, first run)** | | | **~12 min** | **~$28** |
| **Total (2 rounds, cached classify)** | | | **~10 min** | **~$18** |

Assign cost increases each round because Opus refine produces longer
action descriptions. At 2 rounds the total is roughly half the 3-round
cost (~$49) with minimal quality loss (convergence was 15.7% → 5.5%
between rounds 2 and 3).

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
