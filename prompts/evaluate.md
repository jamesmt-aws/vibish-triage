# Evaluate: Verify Fix Themes for a Single Issue

You will receive:
1. A list of fix themes (from the aggregate step)
2. A single GitHub issue from {{.Project}}
3. The extraction (diagnosis + proposed fixes) for that issue

For each theme that claims to cover this issue, evaluate whether the fix
actually applies. Return ONLY a JSON object with no other text.

## Output Schema

```json
{
  "repo": "org/repo",
  "number": 1234,
  "title": "short issue title",
  "applicable_fixes": [
    {
      "theme_id": "theme-kebab-id",
      "verdict": "yes",
      "reasoning": "Why this fix applies or does not."
    }
  ],
  "unaddressed": false,
  "unaddressed_reason": ""
}
```

### Verdict Values

- **yes**: This fix directly addresses the issue's root cause.
- **partial**: This fix would help but does not fully resolve the issue.
- **no**: This fix was mapped to this issue but does not apply on closer inspection.

If no fix theme covers this issue, set `unaddressed: true` and explain what
would be needed in `unaddressed_reason`.
{{if .DomainContext}}
## Domain Context

{{.DomainContext}}
{{end}}