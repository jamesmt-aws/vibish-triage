# Label

For each extraction, normalize potential_fixes into 1-3 canonical labels.
Parallel Sonnet calls, one per extraction.

## Inputs

- `data/extracted.jsonl`
- Config: domain_context

## Output

`data/labeled.jsonl` — one JSON per line, same order as input:

```json
{
  "number": 1234,
  "labels": ["consolidation-savings-threshold", "drift-ignore-external-mutations"]
}
```

## Label rules

- kebab-case, 3-6 words.
- Name the behavioral decision, not the mechanism or feature area.
- Two issues needing the same code change should get the same label.
- Two issues needing different code changes should get different labels.

## Label convergence

Each call sees one issue, so labels do not converge across calls. In
practice: ~720 unique labels for ~564 issues, most appearing once. The
aggregate step clusters by semantic similarity, so exact convergence is
not required.

## Rejected alternative: vocabulary-based labeling

Haiku reads all extractions and defines a vocabulary; Sonnet assigns against
it. Tested April 2026. Produced 87 labels (too few), degraded evaluation
quality to 10% misattribution (vs 2% without vocabulary). The vocabulary
collapsed distinctions the aggregate step needed.

## Validation

- Each line is valid JSON with fields: number, labels.
- labels is a non-empty array of strings.
- Line count matches `extracted.jsonl`.
