# Report

Diagnose issues opened since the last full run and map them to existing
themes. Parallel Sonnet calls only (extract + assign, no label or Opus).

## Prerequisites

`data/fix-themes.jsonl` must exist from a prior full run.

## Behavior

1. Download current issues to a temp directory.
2. Diff by issue number against `data/issues.jsonl`. Issues present in
   GitHub but absent from the data are new. Issues that were closed and
   reopened will re-appear if their number is missing from the data.
3. Extract each new issue (parallel Sonnet).
4. Assign each to 0-2 existing themes (parallel Sonnet).
5. Write report.

## Output

`data/report-YYYY-MM-DD.md`:

```markdown
# New Issues Report: 2026-04-07

12 new issues since last full run. Mapped to 8 existing themes.

## Summary

| Theme | Rank | New | Total | Severity | Issues |
|-------|------|-----|-------|----------|--------|
| ... |

## Details

### org/repo#2940: Title
**Opened:** 2026-04-05
**Diagnosis:** ...
**Proposed fixes:** ...
**Themes:**
- #3 Theme title (25 total issues, score 75)
```

## Validation

- Report file exists and is non-empty.
- Every new issue appears in the details section.
