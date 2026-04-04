package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jamesmt/vibish-triage/internal/bedrock"
	"github.com/jamesmt/vibish-triage/internal/config"
	"github.com/jamesmt/vibish-triage/internal/inference"
)

// planEvent is a classified event written to plan-events.jsonl.
type planEvent struct {
	Timestamp      string   `json:"ts"`
	Repo           string   `json:"repo"`
	Number         int      `json:"number"`
	Event          string   `json:"event"`
	Kind           string   `json:"kind"`
	Action         string   `json:"action"`
	Priority       string   `json:"priority"`
	Reasoning      string   `json:"reasoning"`
	ThemeIDs       []string `json:"theme_ids"`
	Effort         string   `json:"effort"`
	ReworkGuidance string   `json:"rework_guidance,omitempty"`
	DeferReason    string   `json:"defer_reason,omitempty"`
	Question       string   `json:"question,omitempty"`
	AssigneeHint   string   `json:"assignee_hint,omitempty"`
}

// planClassification is the raw Sonnet response for Pass 1.
type planClassification struct {
	Number         int      `json:"number"`
	Repo           string   `json:"repo"`
	Kind           string   `json:"kind"`
	Action         string   `json:"action"`
	Priority       string   `json:"priority"`
	Effort         string   `json:"effort"`
	Reasoning      string   `json:"reasoning"`
	ThemeIDs       []string `json:"theme_ids"`
	ReworkGuidance string   `json:"rework_guidance"`
	DeferReason    string   `json:"defer_reason"`
	Question       string   `json:"question"`
	AssigneeHint   string   `json:"assignee_hint"`
}

// planAction is one action in the action plan from Pass 2.
type planAction struct {
	ActionID     string   `json:"action_id"`
	Action       string   `json:"action"`
	Priority     string   `json:"priority"`
	Effort       string   `json:"effort"`
	Issues       []int    `json:"issues"`
	IssueCount   int      `json:"issue_count"`
	Description  string   `json:"description"`
	AssigneeHint string   `json:"assignee_hint,omitempty"`
	DeferReason  string   `json:"defer_reason,omitempty"`
	DependsOn    []string `json:"depends_on"`
}

// planSummary is the summary stats written to plan-summary.json.
type planSummary struct {
	TotalIssues     int            `json:"total_issues"`
	ByKind          map[string]int `json:"by_kind"`
	ByAction        map[string]int `json:"by_action"`
	ByPriority      map[string]int `json:"by_priority"`
	ActionPlanCount int            `json:"action_plan_count"`
}

// Plan runs the two-pass planning pipeline.
func Plan(ctx context.Context, cfg *config.Config, dataDir, promptsDir string, timeout time.Duration) error {
	os.MkdirAll(dataDir, 0755)

	// Initialize clients.
	sonnet, err := bedrock.NewClient(ctx, "claude-sonnet")
	if err != nil {
		return fmt.Errorf("creating sonnet client: %w", err)
	}
	opus, err := bedrock.NewClient(ctx, "claude-opus")
	if err != nil {
		return fmt.Errorf("creating opus client: %w", err)
	}
	slog.Info("plan: bedrock clients ready", "sonnet", sonnet.Model(), "opus", opus.Model())

	// Pass 1: Classify (Sonnet, parallel).
	stepCtx, cancel := context.WithTimeout(ctx, timeout)
	events, err := runPlanClassify(stepCtx, sonnet, cfg, dataDir, promptsDir)
	cancel()
	if err != nil {
		return fmt.Errorf("plan classify failed: %w", err)
	}

	// Pass 2: Action Plan (Opus, single call).
	stepCtx, cancel = context.WithTimeout(ctx, timeout)
	actions, err := runPlanActionPlan(stepCtx, opus, dataDir)
	cancel()
	if err != nil {
		return fmt.Errorf("plan action-plan failed: %w", err)
	}

	// Write plan-summary.json.
	if err := writePlanSummary(dataDir, events, actions); err != nil {
		return fmt.Errorf("writing plan summary: %w", err)
	}

	// Validate.
	return validatePlan(dataDir)
}

// runPlanClassify runs Pass 1: one Sonnet call per issue.
func runPlanClassify(ctx context.Context, client inference.Client, cfg *config.Config, dataDir, promptsDir string) ([]planEvent, error) {
	extractions, err := readJSONL(filepath.Join(dataDir, "extracted.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("reading extracted.jsonl: %w", err)
	}
	evaluations, err := readJSONL(filepath.Join(dataDir, "evaluated.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("reading evaluated.jsonl: %w", err)
	}
	if len(extractions) != len(evaluations) {
		return nil, fmt.Errorf("extracted.jsonl has %d lines, evaluated.jsonl has %d", len(extractions), len(evaluations))
	}
	slog.Info("plan-classify: starting", "issues", len(extractions))

	system, err := renderPrompt(filepath.Join(promptsDir, "plan.md"), templateData{
		Project:       cfg.Project,
		DomainContext: cfg.DomainContext,
		IssueCount:    len(extractions),
	})
	if err != nil {
		return nil, fmt.Errorf("rendering plan prompt: %w", err)
	}

	cwnd := newCwndController(initialCwnd, maxCwnd)
	results := make([]json.RawMessage, len(extractions))
	var totalUsage inference.Usage
	var usageMu sync.Mutex
	var completed int64
	var errors int64

	var wg sync.WaitGroup
	for i := range extractions {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			cwnd.acquire()

			// Build user message from extraction + evaluation.
			var extraction string
			if idx < len(extractions) {
				extraction = string(extractions[idx])
			}
			var evaluation string
			if idx < len(evaluations) {
				evaluation = string(evaluations[idx])
			}

			// Extract number and title from the extraction.
			var ext struct {
				Number int    `json:"number"`
				Title  string `json:"title"`
				Repo   string `json:"repo"`
			}
			json.Unmarshal(extractions[idx], &ext)

			user := fmt.Sprintf("Issue #%d: %s\n\nExtraction:\n%s\n\nEvaluation:\n%s",
				ext.Number, ext.Title, extraction, evaluation)

			text, usage, throttled, err := converseWithRetry(ctx, client, system, user)
			usageMu.Lock()
			totalUsage = totalUsage.Add(usage)
			usageMu.Unlock()

			if throttled {
				cwnd.onThrottle()
			} else {
				cwnd.onSuccess()
			}

			if err != nil {
				atomic.AddInt64(&errors, 1)
				slog.Error("plan-classify: call failed", "index", idx, "error", err)
				return
			}
			results[idx] = json.RawMessage(stripCodeFences(text))
			n := atomic.AddInt64(&completed, 1)
			if n%50 == 0 {
				slog.Info("plan-classify: progress", "completed", n, "total", len(extractions))
			}
		}(i)
	}
	wg.Wait()

	slog.Info("plan-classify: done",
		"completed", completed, "errors", errors,
		"input_tokens", totalUsage.InputTokens, "output_tokens", totalUsage.OutputTokens,
		"cost", fmt.Sprintf("$%.4f", totalUsage.Cost()))

	if total := int64(len(extractions)); total > 0 && errors*100/total > 10 {
		slog.Warn("plan-classify: high error rate",
			"errors", errors, "total", total,
			"rate", fmt.Sprintf("%.1f%%", float64(errors)*100/float64(total)))
	}

	// Wrap results into planEvent structs and write plan-events.jsonl.
	now := time.Now().UTC().Format(time.RFC3339)
	var events []planEvent
	var eventResults []json.RawMessage

	for _, raw := range results {
		if raw == nil {
			eventResults = append(eventResults, json.RawMessage("{}"))
			events = append(events, planEvent{})
			continue
		}
		var c planClassification
		if err := json.Unmarshal(raw, &c); err != nil {
			eventResults = append(eventResults, json.RawMessage("{}"))
			events = append(events, planEvent{})
			continue
		}

		ev := planEvent{
			Timestamp:      now,
			Repo:           c.Repo,
			Number:         c.Number,
			Event:          "classified",
			Kind:           c.Kind,
			Action:         c.Action,
			Priority:       c.Priority,
			Reasoning:      c.Reasoning,
			ThemeIDs:       c.ThemeIDs,
			Effort:         c.Effort,
			ReworkGuidance: c.ReworkGuidance,
			DeferReason:    c.DeferReason,
			Question:       c.Question,
			AssigneeHint:   c.AssigneeHint,
		}
		events = append(events, ev)

		b, _ := json.Marshal(ev)
		eventResults = append(eventResults, b)
	}

	if err := writeResults(filepath.Join(dataDir, "plan-events.jsonl"), eventResults); err != nil {
		return nil, err
	}
	slog.Info("plan-classify: wrote plan-events.jsonl", "events", len(eventResults))
	return events, nil
}

// runPlanActionPlan runs Pass 2: single Opus call to consolidate into actions.
func runPlanActionPlan(ctx context.Context, client inference.Client, dataDir string) ([]planAction, error) {
	// Read classified events.
	eventLines, err := readJSONL(filepath.Join(dataDir, "plan-events.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("reading plan-events.jsonl: %w", err)
	}
	// Read themes for context.
	themeLines, err := readJSONL(filepath.Join(dataDir, "fix-themes.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("reading fix-themes.jsonl: %w", err)
	}

	slog.Info("plan-action: starting", "events", len(eventLines), "themes", len(themeLines))

	var eventsText strings.Builder
	for _, line := range eventLines {
		var ev struct{ Number int `json:"number"` }
		if json.Unmarshal(line, &ev) != nil || ev.Number == 0 {
			continue
		}
		eventsText.Write(line)
		eventsText.WriteByte('\n')
	}

	var themesText strings.Builder
	for _, line := range themeLines {
		themesText.Write(line)
		themesText.WriteByte('\n')
	}

	system := `You produce an action plan from classified issues. Each action covers one or more issues.

Merge rules:
- Issues sharing a theme become one action when the fix is the same work.
- Split when fixes are distinct even if the theme is shared.
- Reject and defer actions can batch multiple issues.

Sort output by priority (p0 first) then issue count descending.

Return ONLY a JSONL block. One action per line, each line valid JSON:

{"action_id": "kebab-case-id", "action": "accept", "priority": "p0", "effort": "large", "issues": [1234, 1235], "issue_count": 2, "description": "What to do and why.", "assignee_hint": "", "defer_reason": "", "depends_on": []}

Valid actions: accept, reject, assign_aws, rework, needs_info, defer.
Valid priorities: p0, p1, p2, p3.
Valid efforts: trivial, small, medium, large.`

	user := "## Classified Issues\n\n" + eventsText.String() +
		"\n## Fix Themes\n\n" + themesText.String() +
		"\nProduce the action plan as JSONL. One action per line."

	text, usage, err := inference.Converse(ctx, client, system, user,
		inference.WithMaxTokens(32768))
	if err != nil {
		return nil, fmt.Errorf("action-plan call failed: %w", err)
	}
	slog.Info("plan-action: done",
		"input_tokens", usage.InputTokens, "output_tokens", usage.OutputTokens,
		"cost", fmt.Sprintf("$%.4f", usage.Cost()))

	// Parse the response.
	actionText := stripCodeFences(extractFencedBlock(text, "jsonl"))
	if actionText == "" {
		actionText = stripCodeFences(text)
	}

	var actions []planAction
	var actionResults []json.RawMessage
	for _, line := range strings.Split(actionText, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var a planAction
		if err := json.Unmarshal([]byte(line), &a); err != nil {
			slog.Warn("plan-action: skipping invalid line", "error", err)
			continue
		}
		actions = append(actions, a)
		actionResults = append(actionResults, json.RawMessage(line))
	}

	if err := writeResults(filepath.Join(dataDir, "action-plan.jsonl"), actionResults); err != nil {
		return nil, err
	}
	slog.Info("plan-action: wrote action-plan.jsonl", "actions", len(actions))
	return actions, nil
}

// writePlanSummary computes and writes plan-summary.json.
func writePlanSummary(dataDir string, events []planEvent, actions []planAction) error {
	summary := planSummary{
		TotalIssues:     len(events),
		ByKind:          make(map[string]int),
		ByAction:        make(map[string]int),
		ByPriority:      make(map[string]int),
		ActionPlanCount: len(actions),
	}

	for _, ev := range events {
		if ev.Kind != "" {
			summary.ByKind[ev.Kind]++
		}
		if ev.Action != "" {
			summary.ByAction[ev.Action]++
		}
		if ev.Priority != "" {
			summary.ByPriority[ev.Priority]++
		}
	}

	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dataDir, "plan-summary.json"), data, 0644)
}
