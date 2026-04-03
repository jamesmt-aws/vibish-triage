# Aggregate

Cluster labels into themes, assign issues to themes, rank by severity-weighted
issue count. Three passes: Opus draft, Opus merge, parallel Sonnet assign.

## Inputs

- `data/labeled.jsonl` (for label frequency table)
- `data/extracted.jsonl` (for issue assignment)
- `prompts/aggregate.md` (system prompt template)
- Config: project, domain_context

## Pass 1: Draft themes

Build a label frequency table from `labeled.jsonl`:

```
consolidation-savings-threshold: 23 issues [1440, 1442, ...]
drift-ignore-external-mutations: 9 issues [2934, ...]
```

Single Opus call reads this table and clusters labels into themes.

### Draft theme schema

```json
{
  "theme_id": "cost-benefit-consolidation",
  "title": "Evaluate whether each consolidation move is worth the disruption cost",
  "description": "...",
  "theme_type": "behavioral_change",
  "severity": "high",
  "labels": ["consolidation-savings-threshold", "consolidation-churn-prevention"]
}
```

Theme naming, type classification, and severity definitions live in
`prompts/aggregate.md`. The prompt is the single source of truth for these.

## Pass 2: Merge themes

Single Opus call. Merge themes that would be resolved by the same code
change. Preserve all fields; for merged themes, pick the broadest title,
combine descriptions, use the highest severity, union labels.

## Pass 3: Assign issues

Parallel Sonnet calls. Each call receives a compact theme list (IDs + titles)
and one extraction. Returns 0-2 theme assignments:

```json
{
  "number": 1234,
  "theme_ids": ["cost-benefit-consolidation"],
  "reasoning": "brief explanation"
}
```

## Scoring

```
score = severity_weight x issue_count

severity_weight:  high = 3.0,  medium = 1.0,  low = 0.5
```

### Why not effort?

Effort estimates were included through March 2026. The same theme would rate
low in one run and medium in another, producing 3x score swings from noise.
Removed in commit d186738. Effort is a human judgment call, not an LLM
output.

## Outputs

### `data/fix-themes.jsonl`

```json
{
  "theme_id": "cost-benefit-consolidation",
  "title": "...",
  "description": "...",
  "theme_type": "behavioral_change",
  "severity": "high",
  "issue_numbers": [1440, 1442],
  "issue_count": 25,
  "score": 75.0,
  "sample_what_went_wrong": ["Issue #1440: ...", "Issue #2388: ..."]
}
```

`sample_what_went_wrong`: first two issue diagnoses from `extracted.jsonl`
whose number appears in `issue_numbers`. Selected by issue number order,
not by quality.

### `data/fix-priority.md`

Ranked table and per-theme details.

### `data/draft-themes.jsonl`

Post-merge theme definitions (debugging artifact).

## Validation

- Each theme has: theme_id, title, issue_numbers, issue_count.
- `issue_count` equals `len(issue_numbers)`.
- At least 1 issue per theme (themes with 0 issues are dropped).
- `fix-priority.md` exists and is > 2000 bytes.
