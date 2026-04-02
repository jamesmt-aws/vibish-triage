# Karpenter Issue Backlog: What To Fix First
jamesmt@amazon.com -- 2026/04/01

Karpenter manages the compute layer for a growing share of EKS clusters. When
it makes a bad decision, the blast radius is the cluster's entire node fleet. A
consolidation move might save two cents per hour but restart a service that took
minutes to warm its cache. A provisioning decision might select the wrong
instance type because the cost model ignores reserved instance coverage. A race
condition might violate a disruption budget the operator believed was protecting
a critical deployment. And when any of these happen, the operator often cannot
tell what Karpenter decided or why.

I analyzed 564 open issues from the two Karpenter repositories
(kubernetes-sigs/karpenter and aws/karpenter-provider-aws) to answer two
questions. What changes would resolve the largest number of complaints? And does
the consolidation cost-benefit fix I have in progress rank where I think it
does?

## Approach

The analysis is automated and reproducible. The tool is
[vibish-triage](https://github.com/jamesmt-aws/vibish-triage). Run
`./vibish-triage run --config examples/karpenter.yaml` to reproduce these
results from scratch.

Five steps:

1. **Download.** Fetch all open issues and comments from GitHub. (564 issues,
   18 seconds.)

2. **Extract.** For each issue, diagnose what went wrong and propose 1-3
   behavioral fixes. Parallel Sonnet calls. (564 calls, ~90 seconds, ~$8.)

3. **Label.** Normalize each issue's proposed fixes into short canonical labels
   like `consolidation-savings-threshold` or `drift-ignore-external-mutations`.
   Parallel Sonnet calls. Labels are the atomic unit of clustering: two issues
   that need the same code change get the same label. (564 calls, ~37 seconds,
   ~$1.50.)

4. **Aggregate.** Cluster labels into themes. One Opus call reads a label
   frequency table and produces 40-55 theme definitions, a second Opus call
   merges redundant themes, then parallel Sonnet calls assign each issue to 0-2
   themes. Themes are ranked by severity-weighted issue count: `score =
   severity_weight x issue_count` (high=3, medium=1, low=0.5). (~5 minutes,
   ~$5.)

5. **Evaluate.** For each issue, verify that assigned themes actually address
   the root cause. Parallel Sonnet calls produce verdicts: yes, partial, no, or
   unaddressed. (~90 seconds, ~$8.)

The pipeline iterates steps 4-5 up to three times, stopping when the
misattribution rate (fraction of "no" verdicts) drops below 2%.

Total: ~10 minutes, ~$23 per run.

## Quality

The evaluation step measures its own accuracy. On the best iteration:

- **2% misattribution.** Of 609 issue-theme assignments, 19 were wrong.
- **3% unaddressed.** 19 of 564 issues were not covered by any theme.

These numbers are consistent with the March 2026 analysis of the same backlog
(3.7% misattribution on the best iteration of that run).

## Results

The analysis produced 68 themes. The top 10 cover 60% of the backlog.

### The top 10

| # | Fix | Issues | Severity | Score |
|---|-----|--------|----------|-------|
| 1 | Make scheduling simulation match real kube-scheduler behavior | 41 | high | 123 |
| 2 | Let users control which workloads and nodes are exempt from disruption | 25 | high | 75 |
| 3 | Evaluate whether each consolidation move is worth the disruption cost | 25 | high | 75 |
| 4 | Enforce disruption budgets across all disruption reasons | 20 | high | 60 |
| 5 | Get taint, drain, eviction, and termination ordering right | 20 | high | 60 |
| 6 | Batch and filter drift replacements | 18 | high | 54 |
| 7 | Handle node termination gracefully | 12 | high | 36 |
| 8 | Emit accurate metrics with correct labels | 35 | medium | 35 |
| 9 | Fill documentation gaps | 68 | low | 34 |
| 10 | Surface why disruption is blocked or what decision was made | 34 | medium | 34 |

The top 3 cover 26% of the backlog. Top 5: 37%. Top 10: 60%.

### Three categories

**Karpenter made a wrong decision.** Seven of the top ten (1-7) are behavioral
bugs. The scheduling simulator does not match the real kube-scheduler (#1).
Consolidation executes moves that are not worth the disruption (#3). Disruption
budgets are not enforced for all disruption reasons (#4). Race conditions
between controllers cause incorrect state (#5). Drift replacements happen one
at a time instead of in batches (#6). These are the highest-leverage fixes
because they carry high severity and directly cause the incidents that erode
user trust.

**Users cannot tell what happened.** Three of the top ten (8, 9, 10) are about
legibility. Users file issues because they cannot determine what Karpenter
decided, why, or what to do about it. Better metrics (#8), documentation (#9),
and status conditions (#10) would prevent many issues from being filed and make
the behavioral bugs easier to reproduce. This is the cheapest category: log
lines, event emissions, and markdown files.

**The behavioral fixes now outrank legibility.** The March analysis ranked
observability #1 and documentation #2. This analysis ranks behavioral fixes
1-7. The difference is that the March analysis included effort in the ranking
formula (bang-for-buck = severity x count / effort). Since effort estimates are
unstable between runs, we dropped effort from the formula. The result: high-
severity behavioral fixes rise above high-count, low-severity observability
themes. Both analyses agree on what the problems are. They disagree on ordering
because they weight differently.

### The consolidation fix

The cost-benefit consolidation fix ranks #3 with 25 issues and a score of 75.
The March analysis ranked it #4 with 34 issues. The issue count difference is
partly real (5 issues closed since March, a few new ones opened) and partly
clustering variance (some consolidation-adjacent issues land in the disruption
budget or drift themes depending on the run).

The ranking is stable across runs. Consolidation cost-benefit landed #1, #3,
and #8 across three independent runs on April 1. When it ranked #8, the model
had split it into four sub-themes (savings threshold, pricing inputs, scale
performance, spot diversity) that collectively covered 45 issues. The label
step was added specifically to prevent this kind of mechanism-level splitting,
and in the final run it stayed unified at 25 issues.

### Comparison to March

| Theme | March (569 issues) | April (564 issues) |
|-------|-------------------|-------------------|
| Observability | #1, 57 issues | #8+#10, 69 combined |
| Documentation | #2, 58 issues | #9, 68 issues |
| Consolidation cost-benefit | #4, 34 issues | #3, 25 issues |
| TOCTOU races | #5, 29 issues | #2+#5, 45 combined |
| NodePool limits | #3, 38 issues | Scattered |
| Scheduling simulation | #38, 10 issues | #1, 41 issues |

The largest shift is scheduling simulation, which went from #38 (10 issues) in
March to #1 (41 issues) here. March split this across scheduling simulation,
DaemonSet overhead, and volume handling. The current analysis merges them into
one theme because they all answer the same question: does the provisioner's
bin-packing simulation match what the real kube-scheduler will do? This is the
clustering granularity question. Both analyses identify the same issues. They
disagree on how many themes to use.

## Methodology

### Scoring

Severity weighting: high (downtime, data loss, cost explosion, safety
guarantee violations) = 3x. Medium (degraded performance, suboptimal decisions,
workaround exists) = 1x. Low (cosmetic, documentation, minor inconvenience) =
0.5x.

Effort is not used in ranking. An earlier version of the pipeline included
effort estimates, but the same theme would swing between low and medium across
runs, producing 3x score changes from noise. Effort is too unstable to carry
weight in an automated formula. Engineers reading the output can assess effort
themselves.

### Label step

Each Sonnet call sees one issue and produces 1-3 canonical labels. Labels are
the bridge between per-issue diagnosis and theme clustering. Without labels,
Opus reads 564 free-text extractions and makes ad-hoc granularity decisions.
Different runs draw different boundaries, producing 4-way splits of
consolidation or mega-themes that absorb unrelated issues.

With labels, Opus reads a frequency table: "consolidation-savings-threshold: 23
issues, drift-ignore-external-mutations: 9 issues, ..." The quantitative
evidence anchors granularity. A group of labels totaling 45 issues is hard to
split into four themes by accident.

The labels do not converge perfectly (720 unique labels for 564 issues in the
current run, with 710 appearing only once). Opus clusters by semantic
similarity, not exact string matching, so the long tail does not hurt theme
quality. A vocabulary-based approach was tested and rejected: it collapsed too
aggressively (87 labels) and degraded quality (10% misattribution, 23%
unaddressed).

### Limitations

The pipeline has no access to Karpenter source code. Fix proposals are based on
issue descriptions and comments. The analysis reflects the open backlog as of
April 1, 2026.

### Artifacts

All outputs are in `data/`:

| File | Description |
|------|-------------|
| `issues.jsonl` | 564 open issues with full text and comments |
| `extracted.jsonl` | Per-issue diagnosis and proposed fixes |
| `labeled.jsonl` | Per-issue canonical fix labels |
| `draft-themes.jsonl` | Theme definitions after merge pass |
| `fix-themes.jsonl` | Themes with issue assignments, scores, and counts |
| `fix-priority.md` | Ranked table of themes |
| `evaluated.jsonl` | Per-issue verification of theme assignments |

To reproduce: `go build -o vibish-triage . && ./vibish-triage run --config examples/karpenter.yaml`
