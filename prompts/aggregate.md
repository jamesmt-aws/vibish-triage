# Aggregate: Identify Fix Themes from Issue Diagnoses

You will receive {{.IssueCount}} issue extractions, each with a what_went_wrong
diagnosis and 1-3 proposed fixes. Your job is to find the underlying fix themes:
groups of issues that need the same behavioral change.

## How to Name Themes

Name each theme by the **decision that should change**, not by the feature area
affected. A good theme title is an imperative sentence describing what the
system should do differently.

Good theme names:
- "Evaluate disruption cost before consolidating" (decision: should this move happen?)
- "Count unregistered NodeClaims against limits" (decision: is this node counted?)
- "Surface blocking reasons as status conditions" (decision: is the user told why?)
- "Batch drift replacements instead of replacing one at a time" (decision: how many at once?)

Bad theme names:
- "Improve consolidation" (feature area, not a decision)
- "Fix NodePool limits" (too vague — what decision changes?)
- "Observability improvements" (category, not an action)
- "Expose kubelet configuration fields" (feature request list, not a behavioral change)

If a group of issues are all feature requests for configuration surface area
(e.g., "expose field X", "add option Y"), that is one theme: "Expand
configuration surface for [subsystem]". Do not create one theme per field.

## How to Cluster

Group fixes that describe the same behavioral change even if worded differently.
"Add disruption cost check" and "evaluate whether the move is worth the
disruption" and "compare savings against restart cost" are all the same fix.

An issue can map to multiple themes.

Aim for 40-60 themes. Merge aggressively. If two themes would have the same
engineering owner and the same PR, they are one theme.
{{if .DomainContext}}

## Domain Context

{{.DomainContext}}
{{end}}
{{if .KnownFixes}}

## Known Fix Context

These fixes are already understood. Use them as calibration for theme
granularity and effort estimates.
{{range .KnownFixes}}
> **{{.Title}}** (effort: {{.Effort}})
> {{.Rationale}}
{{end}}
{{end}}

## Effort Scale

- **low**: Change to decision logic within existing code paths.
- **medium**: New logic touching multiple components, maybe new fields.
- **high**: Architectural change or subsystem rework.
