package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jamesmt/vibish-triage/internal/bedrock"
	"github.com/jamesmt/vibish-triage/internal/config"
	"github.com/jamesmt/vibish-triage/internal/inference"
)

// planCache provides content-addressed memoization of classify results.
type planCache struct {
	dir string
}

func newPlanCache(dataDir string) *planCache {
	dir := filepath.Join(dataDir, ".plan-cache")
	os.MkdirAll(dir, 0755)
	return &planCache{dir: dir}
}

func (c *planCache) key(parts ...[]byte) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func (c *planCache) get(key string) (json.RawMessage, bool) {
	data, err := os.ReadFile(filepath.Join(c.dir, key+".json"))
	if err != nil {
		return nil, false
	}
	return json.RawMessage(data), true
}

func (c *planCache) put(key string, data json.RawMessage) {
	os.WriteFile(filepath.Join(c.dir, key+".json"), data, 0644)
}

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

// planClassification is the raw Sonnet response for classification.
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

// actionBreakdown is the Haiku-generated diagnostic for large actions.
type actionBreakdown struct {
	DistinctUpdates   int    `json:"distinct_updates"`
	ObviouslyCorrect  int    `json:"obviously_correct"`
	NeedsDiscussion   int    `json:"needs_discussion"`
	Summary           string `json:"summary"`
}

// planAction is one action in the action plan.
type planAction struct {
	ActionID     string            `json:"action_id"`
	Action       string            `json:"action"`
	Priority     string            `json:"priority"`
	Effort       string            `json:"effort"`
	Issues       []int             `json:"issues"`
	IssueCount   int               `json:"issue_count"`
	Description  string            `json:"description"`
	AssigneeHint string            `json:"assignee_hint,omitempty"`
	DeferReason  string            `json:"defer_reason,omitempty"`
	DependsOn    []string          `json:"depends_on"`
	Breakdown    *actionBreakdown  `json:"breakdown,omitempty"`
}

// planSpread contains diagnostic metrics about action size distribution.
type planSpread struct {
	Gini            float64 `json:"gini"`
	Top5Pct         float64 `json:"top_5_pct"`
	Top10Pct        float64 `json:"top_10_pct"`
	Top20Pct        float64 `json:"top_20_pct"`
	MedianActionSize int    `json:"median_action_size"`
	MaxActionSize    int    `json:"max_action_size"`
}

// planSummary is the summary stats written to plan-summary.json.
type planSummary struct {
	TotalIssues     int            `json:"total_issues"`
	ByKind          map[string]int `json:"by_kind"`
	ByAction        map[string]int `json:"by_action"`
	ByPriority      map[string]int `json:"by_priority"`
	ActionPlanCount int            `json:"action_plan_count"`
	EMIterations    int            `json:"em_iterations"`
	OrphanedIssues  int            `json:"orphaned_issues"`
	Spread          planSpread     `json:"spread"`
}

// actionAssignment is the per-issue result from the E-step.
type actionAssignment struct {
	Number    int      `json:"number"`
	ActionIDs []string `json:"action_ids"`
	Reasoning string   `json:"reasoning"`
}

// Plan runs the planning pipeline: classify, then EM-style action plan.
func Plan(ctx context.Context, cfg *config.Config, dataDir, promptsDir string, timeout time.Duration, maxEMRounds int) error {
	os.MkdirAll(dataDir, 0755)

	sonnet, err := bedrock.NewClient(ctx, "claude-sonnet")
	if err != nil {
		return fmt.Errorf("creating sonnet client: %w", err)
	}
	opus, err := bedrock.NewClient(ctx, "claude-opus")
	if err != nil {
		return fmt.Errorf("creating opus client: %w", err)
	}
	slog.Info("plan: bedrock clients ready", "sonnet", sonnet.Model(), "opus", opus.Model())

	// Phase 1: Classify.
	stepCtx, cancel := context.WithTimeout(ctx, timeout)
	events, err := runPlanClassify(stepCtx, sonnet, cfg, dataDir, promptsDir)
	cancel()
	if err != nil {
		return fmt.Errorf("plan classify failed: %w", err)
	}

	// Phase 2: EM-style action plan.
	stepCtx, cancel = context.WithTimeout(ctx, timeout)
	actions, iterations, orphaned, err := runPlanEM(stepCtx, sonnet, opus, events, dataDir, maxEMRounds)
	cancel()
	if err != nil {
		return fmt.Errorf("plan EM failed: %w", err)
	}

	// Post-hoc: breakdown of top 10 actions.
	haiku, err := bedrock.NewClient(ctx, "claude-haiku")
	if err != nil {
		return fmt.Errorf("creating haiku client: %w", err)
	}
	if err := runBreakdown(ctx, haiku, actions, dataDir); err != nil {
		slog.Warn("breakdown failed, continuing without it", "error", err)
	}

	// Write summary.
	if err := writePlanSummary(dataDir, events, actions, iterations, orphaned); err != nil {
		return fmt.Errorf("writing plan summary: %w", err)
	}

	return validatePlan(dataDir)
}

// runPlanClassify runs Phase 1: one Sonnet call per issue.
func runPlanClassify(ctx context.Context, client inference.Client, cfg *config.Config, dataDir, promptsDir string) ([]planEvent, error) {
	issues, err := readJSONL(filepath.Join(dataDir, "issues.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("reading issues.jsonl: %w", err)
	}
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
	if len(extractions) != len(issues) {
		return nil, fmt.Errorf("extracted.jsonl has %d lines, issues.jsonl has %d", len(extractions), len(issues))
	}
	cache := newPlanCache(dataDir)

	// Check cache hits before starting.
	results := make([]json.RawMessage, len(extractions))
	var toClassify []int
	var cacheHits int64
	for i := range extractions {
		k := cache.key(issues[i], extractions[i], evaluations[i])
		if cached, ok := cache.get(k); ok {
			results[i] = cached
			cacheHits++
		} else {
			toClassify = append(toClassify, i)
		}
	}
	slog.Info("plan-classify: starting", "issues", len(extractions), "cached", cacheHits, "to_classify", len(toClassify))

	if len(toClassify) == 0 {
		slog.Info("plan-classify: all cached, skipping LLM calls")
		goto wrapResults
	}

	{
		system, err := renderPrompt(filepath.Join(promptsDir, "plan.md"), templateData{
			Project:       cfg.Project,
			DomainContext: cfg.DomainContext,
			IssueCount:    len(extractions),
		})
		if err != nil {
			return nil, fmt.Errorf("rendering plan prompt: %w", err)
		}

		cwnd := newCwndController(initialCwnd, maxCwnd)
		var totalUsage inference.Usage
		var usageMu sync.Mutex
		var completed int64
		var errors int64

		var wg sync.WaitGroup
		for _, idx := range toClassify {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				cwnd.acquire()

				var ext struct {
					Number int    `json:"number"`
					Title  string `json:"title"`
					Repo   string `json:"repo"`
				}
				json.Unmarshal(extractions[idx], &ext)

				user := fmt.Sprintf("Issue #%d: %s\n\nRaw issue:\n%s\n\nExtraction:\n%s\n\nEvaluation:\n%s",
					ext.Number, ext.Title, string(issues[idx]), string(extractions[idx]), string(evaluations[idx]))

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
				result := json.RawMessage(stripCodeFences(text))
				results[idx] = result
				// Cache the result.
				k := cache.key(issues[idx], extractions[idx], evaluations[idx])
				cache.put(k, result)
				n := atomic.AddInt64(&completed, 1)
				if n%50 == 0 {
					slog.Info("plan-classify: progress", "completed", n, "total", len(toClassify))
				}
			}(idx)
		}
		wg.Wait()

		slog.Info("plan-classify: done",
			"completed", completed, "errors", errors,
			"input_tokens", totalUsage.InputTokens, "output_tokens", totalUsage.OutputTokens,
			"cost", fmt.Sprintf("$%.4f", totalUsage.Cost()))

		if total := int64(len(toClassify)); total > 0 && errors*100/total > 10 {
			slog.Warn("plan-classify: high error rate",
				"errors", errors, "total", total,
				"rate", fmt.Sprintf("%.1f%%", float64(errors)*100/float64(total)))
		}
	}

wrapResults:
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

		effort := c.Effort
		if effort == "" {
			effort = "small"
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
			Effort:         effort,
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

// runPlanEM runs the EM-style action plan: seed, then iterate assign+refine.
func runPlanEM(ctx context.Context, sonnet, opus inference.Client, events []planEvent, dataDir string, maxRounds int) ([]planAction, int, int, error) {
	// Load themes for seed.
	themeLines, err := readJSONL(filepath.Join(dataDir, "fix-themes.jsonl"))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("reading fix-themes.jsonl: %w", err)
	}

	// Load extractions for E-step context.
	extractions, err := readJSONL(filepath.Join(dataDir, "extracted.jsonl"))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("reading extracted.jsonl: %w", err)
	}

	// Build extraction lookup by issue number.
	extractionByNumber := make(map[int]json.RawMessage)
	for _, raw := range extractions {
		var e struct{ Number int `json:"number"` }
		if json.Unmarshal(raw, &e) == nil && e.Number != 0 {
			extractionByNumber[e.Number] = raw
		}
	}

	// Seed: group by theme + action type.
	actions := seedActions(events, themeLines)
	slog.Info("plan-em: seed", "actions", len(actions))

	const stabilityThreshold = 0.05 // 5%

	var iterations int
	var prevAssignments map[int][]string

	var lastAssignments map[int][]string

	for iter := range maxRounds {
		iterations = iter + 1
		slog.Info("plan-em: iteration", "round", iterations)

		// E-step: assign issues to actions.
		assignments, err := runPlanAssign(ctx, sonnet, events, actions, extractionByNumber)
		if err != nil {
			return nil, iterations, 0, fmt.Errorf("plan assign (round %d): %w", iterations, err)
		}
		lastAssignments = assignments

		// Check convergence.
		if prevAssignments != nil {
			changed := countAssignmentChanges(prevAssignments, assignments)
			rate := float64(changed) / float64(len(events))
			slog.Info("plan-em: convergence", "changed", changed, "total", len(events),
				"rate", fmt.Sprintf("%.1f%%", rate*100))
			if rate < stabilityThreshold {
				slog.Info("plan-em: stable, stopping")
				break
			}
		}
		prevAssignments = assignments

		// M-step: refine actions.
		actions, err = runPlanRefine(ctx, opus, actions, assignments, events)
		if err != nil {
			return nil, iterations, 0, fmt.Errorf("plan refine (round %d): %w", iterations, err)
		}
		slog.Info("plan-em: refined", "actions", len(actions))
	}

	// Always rebuild issue lists from the final assignments.
	actions = assembleActions(actions, lastAssignments)

	// Sort by priority then issue count descending.
	priorityOrder := map[string]int{"p0": 0, "p1": 1, "p2": 2, "p3": 3}
	sort.Slice(actions, func(i, j int) bool {
		pi, pj := priorityOrder[actions[i].Priority], priorityOrder[actions[j].Priority]
		if pi != pj {
			return pi < pj
		}
		return actions[i].IssueCount > actions[j].IssueCount
	})

	// Count orphaned issues.
	covered := make(map[int]bool)
	for _, a := range actions {
		for _, n := range a.Issues {
			covered[n] = true
		}
	}
	orphaned := 0
	for _, ev := range events {
		if ev.Number != 0 && !covered[ev.Number] {
			orphaned++
		}
	}

	// Write action-plan.jsonl.
	var actionResults []json.RawMessage
	for _, a := range actions {
		b, _ := json.Marshal(a)
		actionResults = append(actionResults, b)
	}
	if err := writeResults(filepath.Join(dataDir, "action-plan.jsonl"), actionResults); err != nil {
		return nil, iterations, orphaned, err
	}
	slog.Info("plan-em: wrote action-plan.jsonl", "actions", len(actions), "orphaned", orphaned)

	return actions, iterations, orphaned, nil
}

// seedActions creates initial action drafts from themes + classifications.
func seedActions(events []planEvent, themeLines []json.RawMessage) []planAction {
	// Parse themes.
	type themeInfo struct {
		ThemeID     string `json:"theme_id"`
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	themes := make(map[string]themeInfo)
	for _, raw := range themeLines {
		var t themeInfo
		if json.Unmarshal(raw, &t) == nil && t.ThemeID != "" {
			themes[t.ThemeID] = t
		}
	}

	// Group issues by theme + action type.
	type groupKey struct {
		themeID string
		action  string
	}
	groups := make(map[groupKey][]planEvent)
	var noTheme []planEvent

	for _, ev := range events {
		if ev.Number == 0 {
			continue
		}
		if len(ev.ThemeIDs) == 0 {
			noTheme = append(noTheme, ev)
			continue
		}
		for _, tid := range ev.ThemeIDs {
			groups[groupKey{tid, ev.Action}] = append(groups[groupKey{tid, ev.Action}], ev)
		}
	}

	var actions []planAction
	for key, evs := range groups {
		t := themes[key.themeID]
		desc := t.Title
		if desc == "" {
			desc = key.themeID
		}

		// Use the most common priority and effort from the group.
		priority := majorityVote(evs, func(e planEvent) string { return e.Priority })
		effort := majorityVote(evs, func(e planEvent) string { return e.Effort })

		issueNums := make([]int, len(evs))
		for i, e := range evs {
			issueNums[i] = e.Number
		}

		actions = append(actions, planAction{
			ActionID:    key.themeID + "--" + key.action,
			Action:      key.action,
			Priority:    priority,
			Effort:      effort,
			Issues:      issueNums,
			IssueCount:  len(issueNums),
			Description: desc,
			DependsOn:   []string{},
		})
	}

	// Group no-theme issues by action type.
	noThemeGroups := make(map[string][]planEvent)
	for _, ev := range noTheme {
		noThemeGroups[ev.Action] = append(noThemeGroups[ev.Action], ev)
	}
	for action, evs := range noThemeGroups {
		priority := majorityVote(evs, func(e planEvent) string { return e.Priority })
		effort := majorityVote(evs, func(e planEvent) string { return e.Effort })
		issueNums := make([]int, len(evs))
		for i, e := range evs {
			issueNums[i] = e.Number
		}
		actions = append(actions, planAction{
			ActionID:    "unthemed--" + action,
			Action:      action,
			Priority:    priority,
			Effort:      effort,
			Issues:      issueNums,
			IssueCount:  len(issueNums),
			Description: "Issues without theme assignment: " + action,
			DependsOn:   []string{},
		})
	}

	return actions
}

// majorityVote returns the most common value from events using the given key function.
func majorityVote(events []planEvent, key func(planEvent) string) string {
	counts := make(map[string]int)
	for _, e := range events {
		counts[key(e)]++
	}
	best, bestCount := "", 0
	for k, c := range counts {
		if c > bestCount {
			best, bestCount = k, c
		}
	}
	return best
}

// buildCompactActionContext builds a compact action list for the E-step prompt.
func buildCompactActionContext(actions []planAction) string {
	var sb strings.Builder
	sb.WriteString("Current actions:\n")
	for _, a := range actions {
		fmt.Fprintf(&sb, "- %s [%s, %s]: %s\n", a.ActionID, a.Action, a.Priority, a.Description)
	}
	sb.WriteString("\n")
	return sb.String()
}

// runPlanAssign runs the E-step: parallel Sonnet assigns each issue to 0-2 actions.
func runPlanAssign(ctx context.Context, client inference.Client, events []planEvent, actions []planAction, extractionByNumber map[int]json.RawMessage) (map[int][]string, error) {
	actionContext := buildCompactActionContext(actions)
	slog.Info("plan-assign: starting", "issues", len(events), "actions", len(actions))

	system := "You are assigning classified issues to action items.\n\n" +
		"You will receive a list of action IDs with descriptions, and one classified issue.\n" +
		"Return ONLY a JSON object:\n" +
		"{\"number\": 1234, \"action_ids\": [\"action-id-1\"], \"reasoning\": \"brief explanation\"}\n\n" +
		"Assign the issue to the action whose work would directly resolve it.\n" +
		"Most issues match 1 action. Some match 2. Some match 0.\n" +
		"If no action fits, return an empty action_ids array."

	// Only process events with valid numbers.
	type workItem struct {
		idx   int
		event planEvent
	}
	var work []workItem
	for i, ev := range events {
		if ev.Number != 0 {
			work = append(work, workItem{i, ev})
		}
	}

	cwnd := newCwndController(initialCwnd, maxCwnd)
	results := make([]json.RawMessage, len(work))
	var totalUsage inference.Usage
	var usageMu sync.Mutex
	var completed int64
	var errors int64

	var wg sync.WaitGroup
	for i, w := range work {
		wg.Add(1)
		go func(idx int, ev planEvent) {
			defer wg.Done()
			cwnd.acquire()

			// Build user message: action context + classification + extraction.
			var sb strings.Builder
			sb.WriteString(actionContext)
			sb.WriteString(fmt.Sprintf("Issue #%d: kind=%s action=%s priority=%s\n", ev.Number, ev.Kind, ev.Action, ev.Priority))
			sb.WriteString("Reasoning: " + ev.Reasoning + "\n")
			if ext, ok := extractionByNumber[ev.Number]; ok {
				sb.WriteString("\nExtraction:\n")
				sb.Write(ext)
				sb.WriteByte('\n')
			}

			text, usage, throttled, err := converseWithRetry(ctx, client, system, sb.String())
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
				slog.Error("plan-assign: call failed", "number", ev.Number, "error", err)
				return
			}
			results[idx] = json.RawMessage(stripCodeFences(text))
			n := atomic.AddInt64(&completed, 1)
			if n%50 == 0 {
				slog.Info("plan-assign: progress", "completed", n, "total", len(work))
			}
		}(i, w.event)
	}
	wg.Wait()

	slog.Info("plan-assign: done",
		"completed", completed, "errors", errors,
		"input_tokens", totalUsage.InputTokens, "output_tokens", totalUsage.OutputTokens,
		"cost", fmt.Sprintf("$%.4f", totalUsage.Cost()))

	// Parse assignments into map.
	assignments := make(map[int][]string)
	for _, raw := range results {
		if raw == nil {
			continue
		}
		var compact bytes.Buffer
		if json.Compact(&compact, raw) != nil {
			continue
		}
		var a actionAssignment
		if json.Unmarshal(compact.Bytes(), &a) == nil && a.Number != 0 {
			assignments[a.Number] = a.ActionIDs
		}
	}
	return assignments, nil
}

// runPlanRefine runs the M-step: Opus refines action definitions.
func runPlanRefine(ctx context.Context, client inference.Client, actions []planAction, assignments map[int][]string, events []planEvent) ([]planAction, error) {
	// Rebuild issue lists from assignments.
	actionIssues := make(map[string][]int)
	for num, aids := range assignments {
		for _, aid := range aids {
			actionIssues[aid] = append(actionIssues[aid], num)
		}
	}

	// Build action summaries for Opus.
	var sb strings.Builder
	sb.WriteString("Current actions with assignment counts:\n\n")
	for _, a := range actions {
		issues := actionIssues[a.ActionID]
		fmt.Fprintf(&sb, "- %s [%s, %s, %s] %d issues: %s\n",
			a.ActionID, a.Action, a.Priority, a.Effort, len(issues), a.Description)

		// Include 2-3 sample reasonings.
		eventByNumber := make(map[int]planEvent)
		for _, ev := range events {
			eventByNumber[ev.Number] = ev
		}
		samples := 0
		for _, num := range issues {
			if ev, ok := eventByNumber[num]; ok && ev.Reasoning != "" && samples < 3 {
				fmt.Fprintf(&sb, "  Sample #%d: %s\n", num, ev.Reasoning)
				samples++
			}
		}
	}

	// Find orphaned issues.
	assigned := make(map[int]bool)
	for num := range assignments {
		if len(assignments[num]) > 0 {
			assigned[num] = true
		}
	}
	var orphanSummaries []string
	for _, ev := range events {
		if ev.Number != 0 && !assigned[ev.Number] {
			orphanSummaries = append(orphanSummaries, fmt.Sprintf(
				"#%d [%s, %s, %s]: %s", ev.Number, ev.Kind, ev.Action, ev.Priority, ev.Reasoning))
		}
	}
	if len(orphanSummaries) > 0 {
		sb.WriteString(fmt.Sprintf("\nOrphaned issues (%d, not assigned to any action):\n", len(orphanSummaries)))
		limit := min(len(orphanSummaries), 30)
		for _, s := range orphanSummaries[:limit] {
			sb.WriteString("- " + s + "\n")
		}
		if len(orphanSummaries) > limit {
			fmt.Fprintf(&sb, "- ...and %d more\n", len(orphanSummaries)-limit)
		}
	}

	system := `You refine an action plan by merging, splitting, and improving actions.

Rules:
- Merge actions that cover the same work, even across themes.
- Split actions that are too broad (diverse issue types in one bucket).
- Create new actions for orphaned issues that form a pattern.
- Drop empty actions (0 assigned issues).
- Adjust descriptions, priorities, effort estimates based on the samples.
- Keep action_ids in kebab-case.

Return ONLY a JSONL block wrapped in ` + "```jsonl ... ```" + `. One action per line:

{"action_id": "kebab-id", "action": "accept", "priority": "p1", "effort": "medium", "description": "What to do.", "depends_on": []}

Do not include issue lists — those are rebuilt from the next assignment round.`

	user := sb.String() + "\nRefine the action plan. Return as JSONL."

	text, usage, err := inference.Converse(ctx, client, system, user,
		inference.WithMaxTokens(16384))
	if err != nil {
		return nil, fmt.Errorf("refine call failed: %w", err)
	}
	slog.Info("plan-refine: done",
		"input_tokens", usage.InputTokens, "output_tokens", usage.OutputTokens,
		"cost", fmt.Sprintf("$%.4f", usage.Cost()))

	// Parse refined actions.
	refined := stripCodeFences(extractFencedBlock(text, "jsonl"))
	if refined == "" {
		refined = stripCodeFences(text)
	}

	var newActions []planAction
	for _, line := range strings.Split(refined, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var a planAction
		if err := json.Unmarshal([]byte(line), &a); err != nil {
			slog.Warn("plan-refine: skipping invalid line", "error", err)
			continue
		}
		if a.DependsOn == nil {
			a.DependsOn = []string{}
		}
		newActions = append(newActions, a)
	}

	return newActions, nil
}

// assembleActions rebuilds issue lists on actions from the final assignments.
func assembleActions(actions []planAction, assignments map[int][]string) []planAction {
	actionIssues := make(map[string][]int)
	for num, aids := range assignments {
		for _, aid := range aids {
			actionIssues[aid] = append(actionIssues[aid], num)
		}
	}

	var result []planAction
	for _, a := range actions {
		issues := actionIssues[a.ActionID]
		if len(issues) == 0 {
			continue
		}
		a.Issues = issues
		a.IssueCount = len(issues)
		result = append(result, a)
	}
	return result
}

// countAssignmentChanges counts issues whose action assignments changed between rounds.
func countAssignmentChanges(prev, curr map[int][]string) int {
	changed := 0
	// Check all issues in both maps.
	allIssues := make(map[int]bool)
	for n := range prev {
		allIssues[n] = true
	}
	for n := range curr {
		allIssues[n] = true
	}
	for n := range allIssues {
		p := strings.Join(prev[n], ",")
		c := strings.Join(curr[n], ",")
		if p != c {
			changed++
		}
	}
	return changed
}

// writePlanSummary computes and writes plan-summary.json.
// runBreakdown sends the top 10 actions (by issue count) to Haiku for a
// diagnostic breakdown: how many distinct updates, how many obviously correct.
func runBreakdown(ctx context.Context, client inference.Client, actions []planAction, dataDir string) error {
	// Load extractions for context.
	extractions, err := readJSONL(filepath.Join(dataDir, "extracted.jsonl"))
	if err != nil {
		return err
	}
	extractionByNumber := make(map[int]json.RawMessage)
	for _, raw := range extractions {
		var e struct{ Number int `json:"number"` }
		if json.Unmarshal(raw, &e) == nil && e.Number != 0 {
			extractionByNumber[e.Number] = raw
		}
	}

	// Sort by issue count, take top 10.
	sorted := make([]int, len(actions))
	for i := range sorted {
		sorted[i] = i
	}
	sort.Slice(sorted, func(a, b int) bool {
		return actions[sorted[a]].IssueCount > actions[sorted[b]].IssueCount
	})
	top := 10
	if top > len(sorted) {
		top = len(sorted)
	}

	system := `You break down a large action item into its component parts.

You will receive an action description and a list of issues assigned to it.
Answer two questions:
1. How many distinct pieces of work are in this action?
2. How many issues are obviously correct fixes (docs typos, broken links,
   one-line config changes) that someone could merge without debate?

Return ONLY a JSON object:
{"distinct_updates": 6, "obviously_correct": 42, "needs_discussion": 27, "summary": "Brief breakdown: 3 broken-link batches (trivial), 2 missing guides (medium), 1 policy decision (needs input)"}`

	slog.Info("breakdown: starting", "actions", top)

	for _, idx := range sorted[:top] {
		a := &actions[idx]

		var sb strings.Builder
		fmt.Fprintf(&sb, "Action: %s\nDescription: %s\n%d issues:\n\n", a.ActionID, a.Description, a.IssueCount)
		for _, num := range a.Issues {
			if ext, ok := extractionByNumber[num]; ok {
				var e struct {
					Number int    `json:"number"`
					Title  string `json:"title"`
					WhatWentWrong string `json:"what_went_wrong"`
				}
				if json.Unmarshal(ext, &e) == nil {
					fmt.Fprintf(&sb, "#%d: %s — %s\n", e.Number, e.Title, e.WhatWentWrong)
				}
			}
		}

		text, usage, err := inference.Converse(ctx, client, system, sb.String(),
			inference.WithMaxTokens(1024))
		if err != nil {
			slog.Warn("breakdown: call failed", "action", a.ActionID, "error", err)
			continue
		}
		_ = usage

		var bd actionBreakdown
		cleaned := stripCodeFences(text)
		if json.Unmarshal([]byte(cleaned), &bd) == nil {
			a.Breakdown = &bd
			slog.Info("breakdown", "action", a.ActionID, "distinct", bd.DistinctUpdates,
				"obvious", bd.ObviouslyCorrect, "discuss", bd.NeedsDiscussion)
		}
	}

	// Rewrite action-plan.jsonl with breakdowns.
	var results []json.RawMessage
	for _, a := range actions {
		b, _ := json.Marshal(a)
		results = append(results, b)
	}
	return writeResults(filepath.Join(dataDir, "action-plan.jsonl"), results)
}

func writePlanSummary(dataDir string, events []planEvent, actions []planAction, iterations, orphaned int) error {
	summary := planSummary{
		TotalIssues:     len(events),
		ByKind:          make(map[string]int),
		ByAction:        make(map[string]int),
		ByPriority:      make(map[string]int),
		ActionPlanCount: len(actions),
		EMIterations:    iterations,
		OrphanedIssues:  orphaned,
		Spread:          computeSpread(actions),
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

// computeSpread calculates Gini coefficient and top-N coverage for action sizes.
func computeSpread(actions []planAction) planSpread {
	if len(actions) == 0 {
		return planSpread{}
	}

	// Collect sizes sorted ascending for Gini.
	sizes := make([]int, len(actions))
	total := 0
	maxSize := 0
	for i, a := range actions {
		sizes[i] = a.IssueCount
		total += a.IssueCount
		if a.IssueCount > maxSize {
			maxSize = a.IssueCount
		}
	}
	sort.Ints(sizes)

	// Gini coefficient: sum of |xi - xj| for all pairs / (2 * n * sum).
	// Equivalent: G = (2 * sum(i * xi) / (n * sum)) - (n+1)/n
	n := len(sizes)
	var weightedSum float64
	for i, s := range sizes {
		weightedSum += float64(i+1) * float64(s)
	}
	gini := (2*weightedSum/float64(n)/float64(total)) - float64(n+1)/float64(n)
	if gini < 0 {
		gini = 0
	}

	// Top-N coverage: sort descending, count unique issues in top N.
	sort.Sort(sort.Reverse(sort.IntSlice(sizes)))
	topNPct := func(topN int) float64 {
		if topN > len(sizes) {
			topN = len(sizes)
		}
		covered := 0
		for _, s := range sizes[:topN] {
			covered += s
		}
		if total == 0 {
			return 0
		}
		return float64(covered) * 100 / float64(total)
	}

	// Median.
	median := sizes[n/2]
	if n%2 == 0 && n > 1 {
		median = (sizes[n/2-1] + sizes[n/2]) / 2
	}

	return planSpread{
		Gini:             math.Round(gini*100) / 100,
		Top5Pct:          math.Round(topNPct(5)*10) / 10,
		Top10Pct:         math.Round(topNPct(10)*10) / 10,
		Top20Pct:         math.Round(topNPct(20)*10) / 10,
		MedianActionSize: median,
		MaxActionSize:    maxSize,
	}
}
