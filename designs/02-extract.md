# Extract

For each issue, identify the behavioral root cause and propose 1-3 fixes.
Parallel Sonnet calls, one per issue.

## Inputs

- `data/issues.jsonl`
- `prompts/extract.md` (system prompt template)
- Config: project, domain_context

## Output

`data/extracted.jsonl` — one JSON per line, same order as input:

```json
{
  "repo": "org/repo",
  "number": 1234,
  "title": "short title",
  "what_went_wrong": "Behavioral root cause.",
  "potential_fixes": [
    "Proposed behavioral change 1.",
    "Proposed behavioral change 2."
  ]
}
```

## Prompt guidance

- Decompose the complaint into the decision the system made or failed to make.
- Propose fixes that change decisions, not fixes that add knobs.
- Read comments; they often contain the real diagnosis.

## Validation

- Each line is valid JSON with fields: number, what_went_wrong, potential_fixes.
- At least 1 fix per issue.
- Line count matches `issues.jsonl`.
