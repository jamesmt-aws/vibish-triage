# Download

Fetches open issues from configured repos into `issues.jsonl` using `gh`.

## Inputs

Config file: `repos` list, `state` (default: open).

## Outputs

`data/issues.jsonl` — one JSON per line:

```json
{
  "repo": "org/repo",
  "number": 1234,
  "title": "short title",
  "body": "full issue body",
  "labels": ["bug", "area/consolidation"],
  "created_at": "2026-01-15T...",
  "updated_at": "2026-03-28T...",
  "comments_count": 3,
  "url": "https://github.com/org/repo/issues/1234",
  "comments": [
    {"author": "user", "body": "comment text", "created_at": "..."}
  ]
}
```

`data/download-summary.json` — issue counts per repo.

## Caching

Raw API responses cached in `data/.cache/` keyed by issue number +
`updatedAt`. Re-runs only fetch issues that changed.

## Validation

- Each line is valid JSON with fields: repo, number, title, body, updated_at.
- At least 1 issue total.
- `download-summary.json` exists with `total_issues` field.
