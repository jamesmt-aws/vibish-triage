# Evaluate

For each issue, verify whether assigned themes address the root cause.
Parallel Sonnet calls, one per issue.

## Inputs

- `data/issues.jsonl` (full text and comments)
- `data/extracted.jsonl` (diagnoses)
- `data/fix-themes.jsonl` (themes with assignments)
- `prompts/evaluate.md` (system prompt template)
- Config: project, domain_context

## Behavior

Build a reverse index: for each issue, which themes claim it? Send only
relevant themes per call.

## Output

### `data/evaluated.jsonl`

One JSON per issue, same order as input:

```json
{
  "repo": "org/repo",
  "number": 1234,
  "title": "short title",
  "applicable_fixes": [
    {
      "theme_id": "cost-benefit-consolidation",
      "verdict": "yes",
      "reasoning": "This fix directly addresses the root cause."
    }
  ],
  "unaddressed": false,
  "unaddressed_reason": ""
}
```

Verdict values: **yes** (directly addresses root cause), **partial** (helps
but incomplete), **no** (mapped but does not apply).

### `data/evaluation-summary.json`

```json
{
  "total_evaluated": 564,
  "verdicts": {"yes": 273, "partial": 295, "no": 29},
  "unaddressed": 75,
  "misattribution_rate": 0.049,
  "theme_accuracy": {
    "cost-benefit-consolidation": {"yes": 20, "partial": 3, "no": 1}
  }
}
```

## Validation

- Each line is valid JSON with fields: number, applicable_fixes.
- Each fix has theme_id and verdict in {yes, partial, no}.
- Line count matches `issues.jsonl`.
- `evaluation-summary.json` exists with required fields.
