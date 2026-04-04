# vibish-triage

LLM-assisted issue triage for open source projects. Downloads open issues
from GitHub, diagnoses root causes, clusters fixes into themes, and verifies
the clustering. Produces a ranked list of high-leverage fixes scored by
severity-weighted impact: `severity × issue_count`.

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
./vibish-triage run --config examples/karpenter.yaml --step label
./vibish-triage run --config examples/karpenter.yaml --step aggregate
./vibish-triage run --config examples/karpenter.yaml --step evaluate

# After a full run, classify issues and produce an action plan
./vibish-triage plan --config examples/karpenter.yaml
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
```

## Pipeline

Six steps. The first is Go code; the rest are LLM calls via Bedrock.

### 1. Download

Fetches open issues + comments from GitHub via `gh`. Parallel comment
fetching with caching by `updatedAt` timestamp. Re-runs only fetch
issues that changed.

Output: `data/issues.jsonl`

### 2. Extract (parallel Sonnet)

For each issue, diagnoses "what went wrong" (the behavioral decision,
not the surface complaint) and proposes 1-3 fixes.

Output: `data/extracted.jsonl`

### 3. Label (parallel Sonnet)

Normalizes each issue's proposed fixes into short canonical fix labels
(e.g., "consolidation-savings-threshold", "drift-ignore-external-mutations").
Labels are reusable across issues: two issues that need the same code change
get the same label. This produces a bottom-up vocabulary for clustering.

Output: `data/labeled.jsonl`

### 4. Aggregate (three-pass: Opus draft → Opus merge → parallel Sonnet assign)

**Draft themes.** One Opus call reads the label frequency table (label →
issue count) and clusters labels into 40-55 theme definitions. The
quantitative label counts anchor granularity: labels totaling 45 issues
stay as one theme rather than being split by mechanism. Themes are named
by the behavioral decision that should change, classified by type
(`behavioral_change`, `feature_surface`, or `infrastructure`) and
severity (`high`, `medium`, `low`).

**Merge themes.** A second Opus call reviews the draft themes and
merges any that address the same behavioral decision. This prevents
mechanism-level splitting (e.g., five separate consolidation themes
that all answer "should this consolidation move happen?").

**Assign issues.** Parallel Sonnet calls assign each issue to 0-2
themes based on its extraction. Only theme IDs and titles are sent
per-call (not full descriptions) to minimize input tokens.

Themes are ranked by severity-weighted issue count:

```
score = severity_weight × issue_count

severity_weight:  high = 3.0,  medium = 1.0,  low = 0.5
```

This pushes high-severity behavioral fixes above high-count feature-request
buckets. A theme with 15 high-severity issues (score: 45) outranks a theme
with 41 medium-severity feature requests (score: 41).

Output: `data/fix-themes.jsonl`, `data/fix-priority.md`, `data/draft-themes.jsonl`

### 5. Evaluate (parallel Sonnet)

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
| `data/labeled.jsonl` | Per-issue canonical fix labels |
| `data/draft-themes.jsonl` | Theme definitions after merge pass |
| `data/fix-themes.jsonl` | Themes with issue assignments, scores, and counts |
| `data/fix-priority.md` | Ranked table of themes (by severity-weighted impact) |
| `data/evaluated.jsonl` | Per-issue verification of theme assignments |
| `data/plan-events.jsonl` | Per-issue classification (kind, action, priority) |
| `data/action-plan.jsonl` | Consolidated work plan (1 action : N issues) |
| `data/plan-summary.json` | Distribution counts for plan classifications |

## Cost and timing

Measured on Karpenter (564 open issues, April 2026):

| Step | Model | Calls | Time | Cost |
|------|-------|-------|------|------|
| Download | — | — | 18s | $0 |
| Extract | Sonnet | 564 | 90s | $8 |
| Label | Sonnet | 564 | 37s | $1.50 |
| Draft themes | Opus | 1 | 1-2 min | $0.45 |
| Merge themes | Opus | 1 | 1 min | $0.15 |
| Assign issues | Sonnet | 564 | 45s | $4 |
| Evaluate | Sonnet | 564 | 90s | $8 |
| Plan (classify) | Sonnet | 567 | 91s | $9.50 |
| Plan (EM, 3 rounds) | Sonnet+Opus | 1701+3 | 12 min | $39.50 |
| **Total** | | | **~24 min** | **~$71** |

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
--step          Step to run: download, extract, label, aggregate, evaluate, all (default: all)
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
          f'sev={t[\"severity\"]} '
          f'type={t[\"theme_type\"]}')
    print(f'    {t[\"title\"]}')
"
```

### 6. Plan (EM-style: classify + iterative assign/refine)

Classifies every issue (kind, action, priority, effort) from a team review
perspective, then iteratively assigns issues to actions and refines action
boundaries (EM-style). Three phases: Sonnet classifies, code seeds draft
actions from themes, then Sonnet assigns + Opus refines for up to 3 rounds.

The team review perspective is encoded as shared values from four anonymous
reviewers (Mr. Red, Mr. Blue, Mr. Green, Mr. Gold). Values: diagnosis before
action, layer responsibility, silent failure is worst, earned complexity,
existing primitives, tradeoff disclosure, honest uncertainty.

Measured on Karpenter (567 issues): 106 actions, 0 orphans, 3 EM rounds.
49% accept, 40% defer, 5% reject, 5% needs_info. $49, ~16 minutes.

Output: `data/plan-events.jsonl`, `data/action-plan.jsonl`, `data/plan-summary.json`

## Known gaps

The pipeline does not currently produce:
- **Velocity analysis** (monthly open/close rates, backlog growth trend)
- **Per-theme trend analysis** (which themes are accelerating)
- **Recurring issue detection** (closed issues that get re-reported)
- **Per-theme deep dives** (detailed breakdown of verdicts within a theme)

These require downloading closed issues and cross-referencing with
timestamps. Planned for a future iteration.
