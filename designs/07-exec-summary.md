# Executive Summary (optional)

Single Opus call. Produces a 1-2 page summary for a manager making
prioritization and staffing decisions.

## Inputs

- `data/fix-priority.md`
- `data/fix-themes.jsonl`
- `data/evaluation-summary.json`

## Output

`data/exec-summary.md` with three sections:

**1. Headline paragraph.** Issue count, theme count, top 3 actions. An SVP
should understand the situation from this paragraph.

**2. Priority table.** Top 10 fixes. Columns: Priority, Fix, Issues affected,
Severity, Why now (what happens if unfixed).

**3. Categories.** Group top fixes into 2-4 categories by user experience
(not code area). 2-3 sentences each.

## Style

Short sentences. Concrete specifics. Numbers. No filler.

## Validation

- `exec-summary.md` exists.
- Word count between 200 and 800.
