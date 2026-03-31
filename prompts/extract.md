# Extract: Diagnose an Issue and Propose Fixes

You will receive a single GitHub issue from {{.Project}} as JSON. Answer two questions:
1. What went wrong? (the behavioral problem, not the user's surface complaint)
2. What 1-3 things could change to fix it?

Return ONLY a JSON object with no other text. No explanation, no preamble.

## Output Schema

```json
{
  "repo": "org/repo",
  "number": 1234,
  "title": "short issue title",
  "what_went_wrong": "Root cause diagnosis.",
  "potential_fixes": [
    "Proposed behavioral change 1.",
    "Proposed behavioral change 2."
  ]
}
```

Copy repo, number, and title from the input. Write what_went_wrong and potential_fixes.
{{if .DomainContext}}
## Domain Context

{{.DomainContext}}
{{end}}
## How to Think About "What Went Wrong"

Decompose the complaint into the behavioral decision the system made (or failed
to make). Users describe symptoms. Identify the decision.

- "X is too aggressive" -> What decision lacks a cost-benefit check?
- "wrong Y selected" -> What information is the decision missing?
- "Z happens too often" -> What triggers Z, and should it batch or debounce?

## How to Think About Fixes

Propose changes to behavior, not user-facing knobs. Good fixes change a
decision. Bad fixes add a knob that lets users work around a bad decision.

If you genuinely cannot identify a behavioral fix, say so. Some issues are
feature requests for capabilities that do not exist yet. Describe what
capability is missing.
