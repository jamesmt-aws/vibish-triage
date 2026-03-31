# Executive Summary: Karpenter Issue Backlog Triage

Write a 1-2 page executive summary of the Karpenter issue backlog analysis
for an L6 SDM who owns Karpenter. The audience makes prioritization and
staffing decisions. They need to know what to work on, in what order, and why.

## Inputs

- `fix-priority.md` -- ranked list of fix themes with issue counts and effort.
- `fix-themes.jsonl` -- structured theme data with issue numbers.
- `evaluated.jsonl` -- per-issue verification of theme assignments.

## Output

`exec-summary.md` -- a document with three sections:

### 1. One-paragraph summary

State the headline: how many open issues, how many themes, and what the
top 3 actions are. No jargon. An SVP should be able to read this paragraph
and understand the situation.

### 2. Recommended priorities (table)

A table of the top 10 fixes ranked by impact/effort. Columns:
- Priority (1-10)
- Fix (imperative sentence)
- Issues affected
- Effort (low/medium/high)
- Why now (1 sentence: what happens if this isn't fixed)

### 3. Three categories

Group the top fixes into 2-4 categories that map to how the SDM would
assign work. Examples: "users can't tell what happened," "the system
made a wrong decision," "missing configuration surface area." Name the
categories by the user experience, not the code area.

For each category, write 2-3 sentences explaining the pattern and what
resolving it would change for users.

## Style

- Short sentences. No filler.
- Concrete specifics over abstractions.
- Numbers wherever possible.
- No self-congratulatory language.
- No forward references or "as described in Section X."

## Infrastructure Setup

```bash
cp deps/data/fix-priority.md project/
cp deps/data/fix-themes.jsonl project/
cp deps/data/evaluated.jsonl project/
test -s project/fix-priority.md || { echo "fix-priority.md not found"; exit 1; }
test -s project/fix-themes.jsonl || { echo "fix-themes.jsonl not found"; exit 1; }
test -s project/evaluated.jsonl || { echo "evaluated.jsonl not found"; exit 1; }
```

## Validation

```bash
test -s exec-summary.md || { echo "exec-summary.md missing"; exit 1; }
wc -w exec-summary.md | awk '{if ($1 < 200 || $1 > 800) {print "word count out of range: " $1; exit 1}}'
```

## Tools

- python3
