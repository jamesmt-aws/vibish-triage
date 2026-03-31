# vibish-triage

LLM-assisted issue triage for open source projects. Downloads open issues
from GitHub, diagnoses root causes, clusters fixes into themes, and verifies
the clustering. Produces a ranked list of high-leverage fixes scored by
bang-for-buck: `severity × issue_count / effort`.

## Prerequisites

- Go 1.25+
- `gh` CLI, authenticated (`gh auth login`)
- AWS credentials with Bedrock access (Claude Sonnet and Opus via
  cross-region inference profiles)

## Quick start

```bash
go build -o vibish-triage .

# Run the full pipeline on Karpenter
./vibish-triage run --config examples/karpenter.yaml

# Or run one step at a time
./vibish-triage run --config examples/karpenter.yaml --step download
./vibish-triage run --config examples/karpenter.yaml --step extract
./vibish-triage run --config examples/karpenter.yaml --step aggregate
./vibish-triage run --config examples/karpenter.yaml --step evaluate
```

## Config

Create a `triage.yaml` for your project:

```yaml
project: my-project
repos:
  - org/repo-a
  - org/repo-b
state: open
domain_context: |
  Describe what the project does and its key subsystems.
  This context is included in every LLM prompt to ground
  the diagnoses in domain knowledge.
known_fixes:
  - title: "A fix you already understand"
    effort: low
    rationale: "Used as a calibration point for effort estimates."
```

The `known_fixes` field is important: it anchors effort estimates. If you
have a fix that you know is low effort (e.g., a single check in an existing
decision path), include it. The model uses it to calibrate all other effort
ratings.

## Pipeline

Four steps. The first is Go code; the rest are LLM calls via Bedrock.

### 1. Download

Fetches open issues + comments from GitHub via `gh`. Parallel comment
fetching with caching by `updatedAt` timestamp. Re-runs only fetch
issues that changed.

Output: `data/issues.jsonl`

### 2. Extract (parallel Sonnet)

For each issue, diagnoses "what went wrong" (the behavioral decision,
not the surface complaint) and proposes 1-3 fixes.

Output: `data/extracted.jsonl`

### 3. Aggregate (three-pass: Opus draft → Opus merge → parallel Sonnet assign)

**Draft themes.** One Opus call reads all extractions and produces
40-55 theme definitions. Themes are named by the behavioral decision
that should change, not by the feature area affected. Each theme is
classified by type (`behavioral_change`, `feature_surface`, or
`infrastructure`), severity (`high`, `medium`, `low`), and effort.

**Merge themes.** A second Opus call reviews the draft themes and
merges any that address the same behavioral decision. This prevents
mechanism-level splitting (e.g., five separate consolidation themes
that all answer "should this consolidation move happen?").

**Assign issues.** Parallel Sonnet calls assign each issue to 0-2
themes based on its extraction. Only theme IDs and titles are sent
per-call (not full descriptions) to minimize input tokens.

Themes are ranked by bang-for-buck score:

```
score = severity_weight × issue_count / effort_divisor

severity_weight:  high = 3.0,  medium = 1.0,  low = 0.5
effort_divisor:   low  = 1.0,  medium = 3.0,  high = 9.0
```

This pushes low-effort behavioral fixes above high-count feature-request
buckets. A theme with 14 high-severity issues at low effort (score: 42)
outranks a theme with 45 medium-severity issues at medium effort (score: 15).

Output: `data/fix-themes.jsonl`, `data/fix-priority.md`, `data/draft-themes.jsonl`

### 4. Evaluate (parallel Sonnet)

For each issue, verifies that assigned themes actually address the root
cause. Only relevant themes are sent per-call (not all themes). Produces
verdicts: yes, partial, no, or unaddressed.

Output: `data/evaluated.jsonl`

### Iteration

When running `--step all`, the pipeline iterates aggregate + evaluate
up to 3 times. After each round, it computes the misattribution rate
(fraction of "no" verdicts). If below 2%, it stops. Evaluation feedback
(misattributed and unaddressed issues) is passed to the next aggregate
round.

## Outputs

| File | Description |
|------|-------------|
| `data/issues.jsonl` | Raw issues from GitHub |
| `data/extracted.jsonl` | Per-issue diagnosis and proposed fixes |
| `data/draft-themes.jsonl` | Theme definitions after merge pass |
| `data/fix-themes.jsonl` | Themes with issue assignments, scores, and counts |
| `data/fix-priority.md` | Ranked table of themes (by bang-for-buck score) |
| `data/evaluated.jsonl` | Per-issue verification of theme assignments |

## Cost and timing

Measured on Karpenter (567 open issues, March 2026):

| Step | Model | Calls | Time | Cost |
|------|-------|-------|------|------|
| Download | — | — | 18s | $0 |
| Extract | Sonnet | 567 | 90s | $8 |
| Draft themes | Opus | 1 | 2-3 min | $1 |
| Merge themes | Opus | 1 | 2 min | $0.20 |
| Assign issues | Sonnet | 567 | 45s | $4 |
| Evaluate | Sonnet | 567 | 90s | $8 |
| **Total** | | | **~10 min** | **~$21** |

## Theme naming

The aggregate prompt enforces a "Level of Abstraction Test": themes must
be named by the behavioral decision that should change, not by the
mechanism or feature area.

Good: "Only execute disruption moves that are worth the cost"
Bad: "Fix multi-node consolidation candidate selection"

The merge pass catches cases where the draft step produces multiple
themes that all answer the same question (e.g., five ways consolidation
goes wrong → one theme about whether consolidation should happen).

## Concurrency control

Parallel LLM calls use TCP-style slow start (cwnd controller). Starts
at 4 concurrent requests, doubles on success, halves on throttle. This
avoids the thundering-herd problem of launching all requests at once
and hitting Bedrock rate limits.

## Flags

```
--config        Path to config file (default: triage.yaml)
--step          Step to run: download, extract, aggregate, evaluate, all (default: all)
--timeout       Max time per LLM step (default: 90m)
--data-dir      Directory for input/output data (default: ./data)
--prompts-dir   Directory containing prompt templates (default: ./prompts)
--workers       Parallel workers for downloading comments (default: 16)
```

## Customizing prompts

Prompts live in `prompts/` as Go templates. Variables available:

| Variable | Source |
|----------|--------|
| `{{.Project}}` | `project` in config |
| `{{.DomainContext}}` | `domain_context` in config |
| `{{.KnownFixes}}` | `known_fixes` in config |
| `{{.IssueCount}}` | Counted from `issues.jsonl` |

## Verifying results

After a run, check quality:

```bash
# Misattribution rate (should be <10%)
python3 -c "
import json
yes = partial = no = 0
for line in open('data/evaluated.jsonl'):
    line = line.strip()
    if not line: continue
    try: obj = json.loads(line)
    except: continue
    for f in obj.get('applicable_fixes', []):
        v = f['verdict']
        if v == 'yes': yes += 1
        elif v == 'partial': partial += 1
        elif v == 'no': no += 1
total = yes + partial + no
print(f'yes={yes} partial={partial} no={no}')
print(f'misattribution: {no}/{total} = {no*100//total}%')
"

# Unaddressed issues (should be <20%)
python3 -c "
import json
u = t = 0
for line in open('data/evaluated.jsonl'):
    line = line.strip()
    if not line: continue
    try: obj = json.loads(line)
    except: continue
    t += 1
    if obj.get('unaddressed'): u += 1
print(f'unaddressed: {u}/{t} = {u*100//t}%')
"

# Top 10 by score
python3 -c "
import json
themes = [json.loads(l) for l in open('data/fix-themes.jsonl') if l.strip()]
for i, t in enumerate(themes[:10]):
    print(f'{i+1:2d}. [{t[\"issue_count\"]:3d}] score={t[\"score\"]:.0f} '
          f'sev={t[\"severity\"]} effort={t[\"effort_estimate\"]} '
          f'type={t[\"theme_type\"]}')
    print(f'    {t[\"title\"]}')
"
```

## Known gaps

The pipeline does not currently produce:
- **Velocity analysis** (monthly open/close rates, backlog growth trend)
- **Per-theme trend analysis** (which themes are accelerating)
- **Recurring issue detection** (closed issues that get re-reported)
- **Per-theme deep dives** (detailed breakdown of verdicts within a theme)

These require downloading closed issues and cross-referencing with
timestamps. Planned for a future iteration.
