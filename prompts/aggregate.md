# Aggregate: Cluster Fixes and Rank by Impact

Read the per-issue extractions and cluster the proposed fixes into a ranked
list. Many issues propose similar fixes in different words. Find the underlying
fix themes, count how many issues each theme covers, and rank by coverage.

## Inputs

- `extracted.jsonl` -- {{.IssueCount}} per-issue extractions with what_went_wrong and
  potential_fixes fields.

## Output

Two files:

### `fix-themes.jsonl` -- one JSON per theme, ranked by issue count:

```json
{
  "theme_id": "short-kebab-id",
  "title": "Human-readable fix title",
  "description": "What behavioral change this theme represents and why it matters.",
  "issue_numbers": [1234, 1456, 1789],
  "issue_count": 87,
  "severity_weighted_score": 145.5,
  "sample_what_went_wrong": [
    "Issue #1234: Brief summary of diagnosis...",
    "Issue #1456: Brief summary of diagnosis..."
  ],
  "effort_estimate": "low",
  "effort_rationale": "Why this effort level."
}
```

### `fix-priority.md` -- human-readable ranked list:

```
# Fix Priority

## Coverage Summary

| Rank | Fix | Issues | Severity Score | Effort |
|------|-----|--------|---------------|--------|
| 1 | Fix title | 87 | 145.5 | low |
| 2 | ... | ... | ... | ... |

The top 3 fixes cover N of M issues (X%).
The top 5 fixes cover N of M issues (X%).
The top 10 fixes cover N of M issues (X%).

## Fix Details

### 1. Fix title (87 issues)
...
```

## Domain Context
{{if .DomainContext}}
{{.DomainContext}}
{{end}}
{{if .KnownFixes}}
## Known Fix Context

The following fixes are already understood. Map all issues they cover and use
them as calibration points for effort estimates.
{{range .KnownFixes}}
> **{{.Title}}** (effort: {{.Effort}})
> {{.Rationale}}
{{end}}
{{end}}

## How to Cluster

1. Read all extractions. Collect every potential_fix string.

2. Group fixes that describe the same behavioral change even if worded
   differently.

3. An issue can map to multiple themes. One issue might benefit from both
   better cost modeling and better error reporting.

4. Count issues per theme. Weight by severity if you can infer it from the
   what_went_wrong text (mentions of downtime, data loss, or cost explosion
   = high; mentions of inconvenience or missing features = low).

5. Estimate effort for each theme using a 3-point scale:
   - **low**: Change to decision logic within existing code paths.
   - **medium**: New logic touching multiple components, maybe new fields.
   - **high**: Architectural change or subsystem rework.

6. Rank by severity_weighted_score / effort (where low=1, medium=3, high=9).

## Strategy

This is a reduce step over all extractions.

1. Read extracted.jsonl in batches of ~50. For each batch, collect
   potential_fixes into a running list of themes.

2. After reading all batches, consolidate themes. Merge near-duplicates.

3. For each theme, go back through extracted.jsonl and count which issues
   match.

4. Write fix-themes.jsonl and fix-priority.md.

## Infrastructure Setup

```bash
test -s extracted.jsonl || { echo "extracted.jsonl not found"; exit 1; }
which python3 || { echo "python3 required"; exit 1; }
echo "$(wc -l < extracted.jsonl) extractions to aggregate"
```

## Validation

```bash
test -s fix-themes.jsonl || { echo "fix-themes.jsonl missing or empty"; exit 1; }
test -s fix-priority.md || { echo "fix-priority.md missing or empty"; exit 1; }

python3 -c "
import json

themes = []
for line in open('fix-themes.jsonl'):
    line = line.strip()
    if not line:
        continue
    obj = json.loads(line)
    assert 'theme_id' in obj, 'missing theme_id'
    assert 'title' in obj, 'missing title'
    assert 'issue_numbers' in obj, 'missing issue_numbers'
    assert 'issue_count' in obj, 'missing issue_count'
    assert len(obj['issue_numbers']) >= 1, f'theme {obj[\"theme_id\"]} has no issues'
    assert obj['issue_count'] == len(obj['issue_numbers']), f'count mismatch in {obj[\"theme_id\"]}'
    themes.append(obj)

print(f'Validated {len(themes)} fix themes')
assert len(themes) >= 5, f'Expected at least 5 themes, got {len(themes)}'

all_issues = set()
for t in themes:
    all_issues.update(t['issue_numbers'])
print(f'{len(all_issues)} unique issues covered by fix themes')

import os
size = os.path.getsize('fix-priority.md')
assert size > 2000, f'fix-priority.md too small ({size} bytes)'
print(f'fix-priority.md is {size} bytes')
"
```

## Tools

- python3
