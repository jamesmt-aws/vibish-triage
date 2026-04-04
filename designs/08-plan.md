# Plan

Classify every open issue and produce a work plan. Three phases: classify
issues, then iteratively assign them to actions and refine the actions
(EM-style), then produce a constrained proposal.

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
issues) that are needed to classify `has_obvious_rfc` correctly.

The system prompt encodes the team review perspective as shared values.
One consensus position per issue. No individual opinions.

Each call returns a classification.

**Memoization.** Classify results are cached by a hash of (issue +
extraction + evaluation). On re-run, only issues whose inputs changed
get re-classified. Cache lives in `data/.plan-cache/`.

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

Note: `rework` is reserved for future PR planning.

**severity** -- factual claim about the failure mode, not a recommendation
about what to work on:

| Severity | Signal |
|----------|--------|
| `high` | Violates safety guarantees, causes data loss, cost explosion, or silent corruption |
| `medium` | Degraded performance, suboptimal decisions, workaround exists |
| `low` | Cosmetic, documentation, minor inconvenience |

**signals** -- factual data extracted from the issue:

| Signal | Type | Source |
|--------|------|--------|
| `age_days` | int | Days since issue was created |
| `comment_count` | int | Number of comments on the issue |
| `has_workaround` | bool | Whether a workaround is mentioned in the issue or comments |
| `blocks_other_issues` | bool | Whether other issues reference this as a blocker |

**effort**: trivial, small, medium, large.

### Phase 2: Action Plan (EM-style iteration)

Three sub-steps that iterate until stable.

#### Seed

Code assembles draft actions from themes + classifications. For each
theme in fix-themes.jsonl, group classified issues by action type
(accept, defer, reject, etc). Each group becomes a draft action.

The seed is deterministic code, no LLM call.

Issues without theme assignments are grouped by action type into
`unthemed--{action}` buckets. This is expected -- the upstream aggregate
step does not assign every issue to a theme.

#### E-step: Assign (Sonnet, parallel)

For each classified issue, send: the classification, the extraction, and
a compact list of current action drafts (IDs + descriptions only). The
model assigns the issue to 0-2 actions.

#### M-step: Refine (Opus, single call)

Opus receives: all action drafts with their assigned issue counts, sample
reasonings (2-3 per action), and orphaned issue summaries.

Opus refines the action list:
- Merge actions that cover the same work (even across themes)
- Split actions that are too broad (diverse issue types in one bucket)
- Create new actions for orphaned issues that form a pattern
- Adjust descriptions, severity, effort estimates
- Drop empty actions (no assignments)

#### Convergence

Default: 2 rounds (`--max-em-rounds`). Stop early if assignment is
stable (< 5% of issues change action between rounds).

#### Post-hoc: Breakdown (Haiku, top 10 actions)

After the EM loop, Haiku analyzes each of the top 10 actions (by issue
count): how many distinct pieces of work, how many obviously correct
fixes, how many need discussion.

### Phase 3: Proposal (Opus, single call)

One Opus call reads the full action plan and produces a constrained
proposal in `data/proposal.md`:

1. **~10 obvious things to do.** Actions where the classification, severity,
   and signals all point the same direction. No judgment call needed.

2. **~5 you would regret not doing.** A subset of the above (or beyond)
   where inaction has a cost that compounds: safety violations without
   workarounds, bugs that erode trust, gaps that block downstream work.

3. **Top 3 and why.** The three actions the team should start with, with
   explicit reasoning about why these three over the others. The scarcity
   is the point -- it forces the tradeoffs to be visible.

The proposal is a conversation starter, not a directive. It exists to
make the tradeoffs legible for someone who influences a team but does
not control it.

### Summary

After all phases, compute and write plan-summary.json.

## Output

### `data/plan-events.jsonl`

Log-structured. Every line is an immutable event.

```json
{"ts": "2026-04-04T14:30:00Z", "repo": "org/repo", "number": 1234, "event": "classified", "kind": "bug_fix", "action": "assign_aws", "severity": "high", "reasoning": "...", "theme_ids": ["cost-benefit-consolidation"], "effort": "small", "assignee_hint": "scheduling", "signals": {"age_days": 120, "comment_count": 8, "has_workaround": false, "blocks_other_issues": true}}
```

### `data/action-plan.jsonl`

One action per line, sorted by issue count descending.

```json
{"action_id": "cost-benefit-consolidation--accept", "action": "accept", "severity": "high", "effort": "large", "issues": [1234, 1235, 1240], "issue_count": 3, "description": "...", "signals_summary": {"median_age_days": 90, "total_comments": 24, "pct_no_workaround": 67}, "depends_on": []}
```

### `data/proposal.md`

Markdown. Three sections: ~10 obvious, ~5 regret, top 3 with reasoning.

### `data/plan-summary.json`

Counts by kind, action, severity, EM iteration stats, and spread metrics.

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

## Validation

### Structural
- Each line in `plan-events.jsonl` is valid JSON with fields: ts, repo, number, event, kind, action, severity, effort.
- `kind` in {bug_fix, small_change, needs_rfc, has_obvious_rfc, wont_do}.
- `action` in {accept, reject, assign_aws, rework, needs_info, defer}.
- `severity` in {high, medium, low}.
- `effort` in {trivial, small, medium, large}.
- Line count matches `issues.jsonl`.
- `action-plan.jsonl` exists with at least 10 actions.
- Every issue in `action-plan.jsonl` appears in `plan-events.jsonl`.
- `plan-summary.json` exists.
- `proposal.md` exists.
- No real reviewer names in any output.

### Distribution
- `wont_do` is less than 40% of issues.
- `needs_info` is less than 15% of issues.
- Every kind has at least 1 issue.
- Orphaned issues (not in any action) less than 10%.
