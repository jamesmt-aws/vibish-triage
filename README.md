# vibish-triage

LLM-assisted issue triage for open source projects. Downloads open issues
from GitHub, diagnoses root causes, clusters fixes into themes, and verifies
the clustering. Produces a ranked list of high-leverage fixes.

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

## Pipeline

Four steps. The first is Go code; the rest are LLM calls via Bedrock.

### 1. Download

Fetches open issues + comments from GitHub via `gh`. Parallel comment
fetching with caching by `updatedAt` timestamp. Re-runs only fetch
issues that changed.

Output: `data/issues.jsonl`

### 2. Extract (parallel Sonnet)

For each issue, diagnoses "what went wrong" (the behavioral decision,
not the surface complaint) and proposes 1-3 fixes. 568 parallel calls
with TCP slow-start concurrency control.

Output: `data/extracted.jsonl`

### 3. Aggregate (two-pass: Opus draft + parallel Sonnet assign)

**Draft themes.** One Opus call reads all extractions and produces
40-60 theme definitions. Themes are named by the behavioral decision
that should change, not by the feature area affected.

**Assign issues.** Parallel Sonnet calls assign each issue to 0-2
themes based on its extraction. Only theme IDs and titles are sent
per-call (not full descriptions) to minimize input tokens.

The two outputs are assembled into ranked theme files.

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
| `data/draft-themes.jsonl` | Theme definitions (no issue assignments) |
| `data/fix-themes.jsonl` | Themes with issue assignments and counts |
| `data/fix-priority.md` | Ranked table of themes |
| `data/evaluated.jsonl` | Per-issue verification of theme assignments |

## Cost and timing

Measured on Karpenter (568 open issues, March 2026):

| Step | Model | Calls | Time | Cost |
|------|-------|-------|------|------|
| Download | — | — | 18s | $0 |
| Extract | Sonnet | 568 | 35s | $8 |
| Draft themes | Opus | 1 | 3-10 min | $1-2 |
| Assign issues | Sonnet | 568 | 45s | $5 |
| Evaluate | Sonnet | 568 | 90s | $8 |
| **Total** | | | **~15 min** | **~$22** |

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
    for f in json.loads(line).get('applicable_fixes', []):
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
u = sum(1 for line in open('data/evaluated.jsonl')
        if json.loads(line).get('unaddressed'))
t = sum(1 for _ in open('data/evaluated.jsonl'))
print(f'unaddressed: {u}/{t} = {u*100//t}%')
"

# Spot-check: pick an issue you know, check its diagnosis
python3 -c "
import json
for line in open('data/extracted.jsonl'):
    obj = json.loads(line)
    if obj['number'] == 2814:  # replace with your issue
        print(json.dumps(obj, indent=2))
        break
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
