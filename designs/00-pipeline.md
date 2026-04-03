# Pipeline

## Steps

1. [Download](01-download.md) — fetch issues from GitHub
2. [Extract](02-extract.md) — diagnose each issue (Sonnet, parallel)
3. [Label](03-label.md) — normalize fixes into canonical labels (Sonnet, parallel)
4. [Aggregate](04-aggregate.md) — cluster labels into themes (Opus + Sonnet)
5. [Evaluate](05-evaluate.md) — verify theme assignments (Sonnet, parallel)
6. [Report](06-report.md) — incremental triage of new issues (Sonnet, parallel)
7. [Exec Summary](07-exec-summary.md) — summary for managers (Opus, optional)

## CLI

```
./vibish-triage run --config triage.yaml                 # steps 1-5
./vibish-triage run --config triage.yaml --step download  # step 1 only
./vibish-triage run --config triage.yaml --step extract   # step 2 only
./vibish-triage run --config triage.yaml --step label     # step 3 only
./vibish-triage run --config triage.yaml --step aggregate # step 4 only
./vibish-triage run --config triage.yaml --step evaluate  # step 5 only
./vibish-triage report --config triage.yaml               # step 6
```

Step 7 is not yet implemented.

## Iteration

`--step all` iterates steps 4-5 up to 3 times:

1. Run aggregate (draft → merge → assign).
2. Run evaluate.
3. Compute misattribution rate = no / (yes + partial + no).
4. If below 2%, stop.
5. Otherwise, summarize misattributed and unaddressed issues, pass as
   feedback to the next aggregate round.

Label (step 3) runs once. Extract (step 2) runs once. Only aggregate and
evaluate repeat.

## Concurrency control

Parallel Sonnet calls use a cwnd controller modeled on TCP slow start.
Starts at 4 concurrent requests, doubles on success, halves on throttle
(429). Max 64. This avoids thundering-herd problems with Bedrock rate
limits.

Used by: extract, label, assign (within aggregate), evaluate, report.

## Config

```yaml
project: my-project
repos:
  - org/repo-a
  - org/repo-b
state: open
domain_context: |
  Describe what the project does and its key subsystems.
```

`domain_context` is included in every LLM prompt.

## Cost

For a project with ~564 issues, one full run costs ~$22 and takes ~8
minutes. Weekly reports cost ~$0.02 per new issue.
