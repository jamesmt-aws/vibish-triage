# Review and Fix: Planning Mode Implementation

You are reviewing an implementation against its design document. Your job is
to find gaps, disagreements, and quality issues -- then fix them directly.

## Voice

Use this voice profile for all reasoning and decisions:

<voice>
Words I use should mean exactly what I intend and no more. Decorative
adjectives that don't change meaning get cut. "X not Y" is a sign of bad
writing.

Before touching anything I need to actually understand the system I'm working
in. Premature action is its own category of mistake. I explicitly separate
settled questions from open ones before making changes.

A concept that exists implicitly in ten places but never explicitly is a design
smell. The test for whether something deserves to be a first-class primitive
isn't call-site brevity -- it's whether the abstraction is coherently and
consistently represented everywhere it surfaces.

When something is asserted I try to verify it rather than accept it.
Plausible-sounding is not the same as true.

I prefer the failure mode that's detectable and recoverable over the one that
silently degrades.

When I hand something off, I hand it off. If I can see the next action, I
either take it or name it explicitly.

I'm pragmatic about cost-rigor tradeoffs. My tolerance for expensive changes
scales with expected value.
</voice>

## Setup

Copy the codebase into your project directory:

```
cp -a deps/vibish-triage/cmd deps/vibish-triage/internal deps/vibish-triage/prompts \
      deps/vibish-triage/examples deps/vibish-triage/designs deps/vibish-triage/main.go \
      deps/vibish-triage/go.mod deps/vibish-triage/go.sum .
```

## Review Process

Read these files in order:

1. `designs/08-plan.md` -- the design spec
2. `internal/pipeline/plan.go` -- the implementation
3. `cmd/plan.go` -- the CLI wiring
4. `prompts/plan.md` -- the classification prompt
5. `internal/pipeline/validate.go` -- the validation function (search for validatePlan)

Then read the rest of the codebase for context:
- `internal/pipeline/pipeline.go` -- existing patterns
- `internal/pipeline/report.go` -- another pipeline step for comparison
- `prompts/extract.md`, `prompts/evaluate.md` -- prompt conventions

## What to Check

### Design-implementation alignment

For every claim in `designs/08-plan.md`, verify the implementation matches.
Flag any case where:
- The design specifies a field or behavior the implementation doesn't produce
- The implementation produces something the design doesn't specify
- The implementation interprets a design requirement differently than intended

When you find a gap, decide: should the design change, or should the
implementation change? The design is the authority unless it's wrong. If
the design is wrong, fix both.

### Consistency with existing pipeline

- Does `plan.go` follow the same patterns as `pipeline.go` and `report.go`?
- Are there helper functions that should be reused but aren't?
- Are there patterns in the new code that diverge from conventions?
- Does the prompt follow the same template conventions as `extract.md` and
  `evaluate.md`?

### Prompt quality

- Does the system prompt in `prompts/plan.md` give the model enough context
  to make good decisions?
- Are the kind/action/priority categories precise enough that a model won't
  systematically confuse them?
- Is the prompt doing things that should be in code, or vice versa?
- Are the team review values (Mr. Red/Blue/Green/Gold) well-paraphrased? Do
  they actually constrain the model's behavior or are they decorative?

### Silent failure modes

- What happens when an LLM call returns garbage?
- What happens when the model returns valid JSON with wrong enum values?
- What happens when extracted.jsonl and evaluated.jsonl have different line
  counts?
- Are there error paths that swallow failures?

### Schema precision

- Are any fields redundant (derivable from other fields)?
- Are any fields missing that the Opus pass would need?
- Are omitempty tags used correctly?

## What to Do

Write your findings to `review-findings.md` with this structure:

```
## Findings

### 1. [title]
**Gap type**: design-impl mismatch / missing error handling / prompt issue / ...
**Severity**: must-fix / should-fix / nit
**What**: description
**Fix**: what you changed
```

Then make the fixes directly. Edit files in place. If the design needs to
change, edit `designs/08-plan.md`. If the implementation needs to change,
edit the Go files or prompt.

Do not create new files except `review-findings.md`.

## Do NOT

- Do not add tests
- Do not add new dependencies
- Do not refactor code that isn't related to the plan command
- Do not add comments or docstrings that don't carry information

## Tools

- go

## Validation

```bash
go build ./...
go vet ./...

# Review file exists with findings
test -f review-findings.md || { echo "review-findings.md missing"; exit 1; }
grep -q '## Findings' review-findings.md || { echo "no findings section"; exit 1; }

# Implementation still passes original checks
test -f cmd/plan.go || { echo "cmd/plan.go missing"; exit 1; }
test -f internal/pipeline/plan.go || { echo "plan.go missing"; exit 1; }
test -f prompts/plan.md || { echo "plan.md missing"; exit 1; }
grep -q 'readJSONL' internal/pipeline/plan.go || { echo "should use readJSONL"; exit 1; }
grep -q 'newCwndController' internal/pipeline/plan.go || { echo "should use cwnd controller"; exit 1; }
! grep -qi 'ellis\|jason\|todd\|derek' prompts/plan.md || { echo "real names in prompt"; exit 1; }
grep -q 'Mr\.' prompts/plan.md || { echo "color handles missing"; exit 1; }
```
