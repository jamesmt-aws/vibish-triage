# Implement: Planning Mode

You are working inside a ralphit sandbox. The existing vibish-triage codebase
is symlinked at `deps/vibish-triage/`. Start by copying the entire codebase
into your project directory so you can build on it:

```
cp -a deps/vibish-triage/cmd deps/vibish-triage/internal deps/vibish-triage/prompts \
      deps/vibish-triage/examples deps/vibish-triage/designs deps/vibish-triage/main.go \
      deps/vibish-triage/go.mod deps/vibish-triage/go.sum .
```

Then implement the `plan` command as specified in `designs/08-plan.md`.
Read that design document. Read the existing code to understand conventions.

## What to Build

Three things:

1. **`cmd/plan.go`** -- cobra subcommand `plan` with the same flags as `run`
   (`--config`, `--data-dir`, `--timeout`, `--prompts-dir`).

2. **`internal/pipeline/plan.go`** -- two-pass pipeline:
   - Pass 1: parallel Sonnet, one call per issue, produces classified events.
   - Pass 2: single Opus call, produces action plan.
   - Writes `plan-events.jsonl`, `action-plan.jsonl`, `plan-summary.json`.

3. **`prompts/plan.md`** -- system prompt for the classification pass. Must
   include the team review perspective (see below).

## Implementation Guidance

### Follow existing patterns

Read these files and match their conventions exactly:

- `cmd/run.go` -- for cobra command wiring
- `cmd/report.go` -- for a simpler subcommand example
- `internal/pipeline/pipeline.go` -- for Sonnet parallel calls (cwnd
  controller, converseWithRetry, readJSONL, writeResults, stripCodeFences)
- `internal/pipeline/report.go` -- for a pipeline step that reads prior output
- `prompts/extract.md` and `prompts/evaluate.md` -- for prompt template style

The cwnd controller, converseWithRetry, readJSONL, writeResults, and all
helper functions already exist in `pipeline.go`. Reuse them. Do not
duplicate or reimplement.

### Pass 1: Classify (Sonnet, parallel)

For each issue, build the user message from:
- The extraction from `extracted.jsonl` (same line index as `issues.jsonl`)
- The evaluation from `evaluated.jsonl` (same line index)
- Issue number and title (from the extraction, not re-parsed from issues.jsonl)

The system prompt (`prompts/plan.md`) is a Go template with `{{.Project}}`,
`{{.DomainContext}}`, and `{{.IssueCount}}` variables, same as other prompts.

The system prompt must include the team review perspective. Encode it as
shared values -- NOT four separate opinions. Use Mr. Red, Mr. Blue,
Mr. Green, Mr. Gold as anonymous handles. The values to encode (paraphrase,
do not copy verbatim):

- Diagnosis before action: the symptom is a hypothesis. Verify the diagnosis
  before building on it.
- Layer responsibility: if the problem lives upstream, redirect it. Don't
  solve someone else's problem in your layer.
- Silent failure is worse than loud failure.
- Earned complexity: default to minimal. If something doesn't earn its place,
  remove it.
- Existing primitives before new abstractions.
- Tradeoff disclosure: name the cost of every decision.
- Honest uncertainty: state confidence levels.

The prompt must instruct the model to return JSON matching this schema:

```json
{
  "number": 1234,
  "repo": "org/repo",
  "kind": "bug_fix",
  "action": "accept",
  "priority": "p1",
  "effort": "small",
  "reasoning": "2-3 sentences in team consensus voice.",
  "theme_ids": ["theme-id"],
  "rework_guidance": "",
  "defer_reason": "",
  "question": "",
  "assignee_hint": ""
}
```

Valid kinds: bug_fix, small_change, needs_rfc, has_rfc, wont_do.
Valid actions: accept, reject, assign_aws, rework, needs_info, defer.
Valid priorities: p0, p1, p2, p3.
Valid efforts: trivial, small, medium, large.

### Pass 2: Action Plan (Opus, single call)

Build the user message from:
- All classified events (the full plan-events.jsonl content)
- fix-themes.jsonl (for theme context)

System prompt: instruct Opus to produce one action per line as JSONL,
each action covering 1:N issues. Merge issues that share a theme into one
action when the fix is the same work. Split when fixes are distinct.
Sort by priority then issue count descending.

Action plan schema:

```json
{
  "action_id": "kebab-case-id",
  "action": "assign_aws",
  "priority": "p0",
  "effort": "large",
  "issues": [1234, 1235],
  "issue_count": 2,
  "description": "What to do and why.",
  "assignee_hint": "expertise area",
  "defer_reason": "",
  "depends_on": []
}
```

Use `inference.Converse` with `inference.WithMaxTokens(32768)` for the Opus
call (same pattern as runDraftThemes).

### plan-summary.json

After both passes, compute and write summary stats: counts by kind, action,
priority, plus action_plan_count.

### Validation

After writing all output, run validation checks matching the design doc:
- Structural: valid JSON, required fields, valid enum values, line count match
- Distribution: wont_do < 40%, p0 < 10%, needs_info < 15%, every kind present

Log warnings for distribution violations but do not fail the pipeline.
Structural violations are errors.

Write a `validatePlan` function following the pattern of `validateExtract`,
`validateLabel`, etc. in `internal/pipeline/validate.go`.

### Event format

Wrap each classification result into a classified event before writing:

```go
type planEvent struct {
    Timestamp   string   `json:"ts"`
    Repo        string   `json:"repo"`
    Number      int      `json:"number"`
    Event       string   `json:"event"`       // always "classified"
    Kind        string   `json:"kind"`
    Action      string   `json:"action"`
    Priority    string   `json:"priority"`
    Reasoning   string   `json:"reasoning"`
    ThemeIDs    []string `json:"theme_ids"`
    Effort      string   `json:"effort"`
    ReworkGuidance string `json:"rework_guidance,omitempty"`
    DeferReason    string `json:"defer_reason,omitempty"`
    Question       string `json:"question,omitempty"`
    AssigneeHint   string `json:"assignee_hint,omitempty"`
}
```

Set `Timestamp` to `time.Now().UTC().Format(time.RFC3339)` and `Event` to
`"classified"` when wrapping the Sonnet response.

## Do NOT

- Do not create test files. This codebase has no tests yet.
- Do not modify existing files except to add imports or registration in
  `cmd/root.go` if needed.
- Do not add new dependencies to `go.mod`.
- Do not add the team muse verbatim. Paraphrase the values into the prompt.

## Tools

- go

## Validation

```bash
go build ./...
go vet ./...

# Check files exist
test -f cmd/plan.go || { echo "cmd/plan.go missing"; exit 1; }
test -f internal/pipeline/plan.go || { echo "internal/pipeline/plan.go missing"; exit 1; }
test -f prompts/plan.md || { echo "prompts/plan.md missing"; exit 1; }

# Check cmd/plan.go registers the plan command
grep -q 'planCmd' cmd/plan.go || { echo "planCmd not found in cmd/plan.go"; exit 1; }

# Check plan.go uses existing helpers (not reimplementing)
grep -q 'readJSONL' internal/pipeline/plan.go || { echo "should use readJSONL"; exit 1; }
grep -q 'newCwndController' internal/pipeline/plan.go || { echo "should use cwnd controller"; exit 1; }
grep -q 'converseWithRetry' internal/pipeline/plan.go || { echo "should use converseWithRetry"; exit 1; }

# Check prompt has no real names
! grep -qi 'ellis\|jason\|todd\|derek' prompts/plan.md || { echo "real names found in prompt"; exit 1; }

# Check prompt uses color handles
grep -q 'Mr\.' prompts/plan.md || { echo "color handles not found in prompt"; exit 1; }
```
