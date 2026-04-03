package pipeline

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"text/template"
	"time"

	"github.com/jamesmt/vibish-triage/internal/bedrock"
	"github.com/jamesmt/vibish-triage/internal/inference"
	"github.com/jamesmt/vibish-triage/internal/config"
	"github.com/jamesmt/vibish-triage/internal/download"
)

// Run executes the full pipeline or a single step.
func Run(ctx context.Context, cfg *config.Config, step string, dataDir, promptsDir string, timeout time.Duration, workers int) error {
	os.MkdirAll(dataDir, 0755)

	if step == "all" || step == "download" {
		slog.Info("downloading issues")
		cacheDir := filepath.Join(dataDir, ".cache")
		if err := download.Run(cfg.Repos, cfg.State, dataDir, cacheDir, workers); err != nil {
			return fmt.Errorf("download failed: %w", err)
		}
		if err := validateDownload(dataDir); err != nil {
			return fmt.Errorf("download validation failed: %w", err)
		}
		if step == "download" {
			return nil
		}
	}

	// Initialize Bedrock clients
	sonnet, err := bedrock.NewClient(ctx, "claude-sonnet")
	if err != nil {
		return fmt.Errorf("creating sonnet client: %w", err)
	}
	opus, err := bedrock.NewClient(ctx, "claude-opus")
	if err != nil {
		return fmt.Errorf("creating opus client: %w", err)
	}
	slog.Info("bedrock clients ready", "sonnet", sonnet.Model(), "opus", opus.Model())

	if step == "all" || step == "extract" {
		stepCtx, cancel := context.WithTimeout(ctx, timeout)
		err := runExtract(stepCtx, sonnet, cfg, dataDir, promptsDir)
		cancel()
		if err != nil {
			return fmt.Errorf("extract failed: %w", err)
		}
	}

	if step == "all" || step == "label" {
		stepCtx, cancel := context.WithTimeout(ctx, timeout)
		err := runLabel(stepCtx, sonnet, cfg, dataDir)
		cancel()
		if err != nil {
			return fmt.Errorf("label failed: %w", err)
		}
		if step == "label" {
			return nil
		}
	}

	if step == "all" {
		// Iterate: aggregate -> evaluate -> re-aggregate with feedback
		// Stop when misattribution rate drops below threshold or max iterations.
		const maxIterations = 3
		const misattributionThreshold = 0.02 // 2%

		for iter := range maxIterations {
			slog.Info("iteration", "round", iter+1, "max", maxIterations)

			// Aggregate (with evaluation feedback on rounds > 0)
			var feedback string
			if iter > 0 {
				feedback = summarizeEvalFeedback(filepath.Join(dataDir, "evaluated.jsonl"))
			}
			stepCtx, cancel := context.WithTimeout(ctx, timeout)
			err := runAggregate(stepCtx, opus, sonnet, cfg, dataDir, promptsDir, feedback)
			cancel()
			if err != nil {
				return fmt.Errorf("aggregate (round %d) failed: %w", iter+1, err)
			}

			// Evaluate
			stepCtx, cancel = context.WithTimeout(ctx, timeout)
			err = runEvaluate(stepCtx, sonnet, cfg, dataDir, promptsDir)
			cancel()
			if err != nil {
				return fmt.Errorf("evaluate (round %d) failed: %w", iter+1, err)
			}

			// Check misattribution rate
			stats := computeEvalStats(filepath.Join(dataDir, "evaluated.jsonl"))
			slog.Info("iteration results",
				"round", iter+1,
				"yes", stats.yes, "partial", stats.partial, "no", stats.no,
				"unaddressed", stats.unaddressed,
				"misattribution_rate", fmt.Sprintf("%.1f%%", stats.misattributionRate()*100))

			if stats.total == 0 {
				return fmt.Errorf("evaluate (round %d) produced no results — check credentials and API access", iter+1)
			}

			if stats.misattributionRate() < misattributionThreshold {
				slog.Info("misattribution rate below threshold, stopping iteration")
				break
			}
		}
	}

	if step == "aggregate" {
		stepCtx, cancel := context.WithTimeout(ctx, timeout)
		err := runAggregate(stepCtx, opus, sonnet, cfg, dataDir, promptsDir, "")
		cancel()
		if err != nil {
			return fmt.Errorf("aggregate failed: %w", err)
		}
	}

	if step == "evaluate" {
		stepCtx, cancel := context.WithTimeout(ctx, timeout)
		err := runEvaluate(stepCtx, sonnet, cfg, dataDir, promptsDir)
		cancel()
		if err != nil {
			return fmt.Errorf("evaluate failed: %w", err)
		}
	}

	return nil
}

// templateData is passed to prompt templates.
type templateData struct {
	Project       string
	DomainContext string
	IssueCount    int
}

func renderPrompt(promptPath string, data templateData) (string, error) {
	raw, err := os.ReadFile(promptPath)
	if err != nil {
		return "", err
	}
	tmpl, err := template.New(filepath.Base(promptPath)).Parse(string(raw))
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

const (
	initialCwnd = 4
	maxCwnd     = 64
	maxRetries  = 3
)

// runExtract calls Sonnet once per issue with slow-start concurrency control.
func runExtract(ctx context.Context, client inference.Client, cfg *config.Config, dataDir, promptsDir string) error {
	issues, err := readJSONL(filepath.Join(dataDir, "issues.jsonl"))
	if err != nil {
		return err
	}
	slog.Info("extract: starting", "issues", len(issues))

	system, err := renderPrompt(filepath.Join(promptsDir, "extract.md"), templateData{
		Project:       cfg.Project,
		DomainContext: cfg.DomainContext,
		IssueCount:    len(issues),
	})
	if err != nil {
		return fmt.Errorf("rendering extract prompt: %w", err)
	}

	cwnd := newCwndController(initialCwnd, maxCwnd)
	results := make([]json.RawMessage, len(issues))
	var totalUsage inference.Usage
	var usageMu sync.Mutex
	var completed int64
	var errors int64

	var wg sync.WaitGroup
	for i, issue := range issues {
		wg.Add(1)
		go func(idx int, issueJSON json.RawMessage) {
			defer wg.Done()
			cwnd.acquire()

			text, usage, throttled, err := converseWithRetry(ctx, client, system, string(issueJSON))
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
				slog.Error("extract: call failed", "index", idx, "error", err)
				return
			}
			results[idx] = json.RawMessage(stripCodeFences(text))
			n := atomic.AddInt64(&completed, 1)
			if n%50 == 0 {
				slog.Info("extract: progress", "completed", n, "total", len(issues))
			}
		}(i, issue)
	}
	wg.Wait()

	slog.Info("extract: done",
		"completed", completed, "errors", errors,
		"input_tokens", totalUsage.InputTokens, "output_tokens", totalUsage.OutputTokens,
		"cost", fmt.Sprintf("$%.4f", totalUsage.Cost()))

	if err := writeResults(filepath.Join(dataDir, "extracted.jsonl"), results); err != nil {
		return err
	}
	return validateExtract(dataDir)
}

// runLabel normalizes each issue's potential_fixes into canonical fix labels.
// Output: data/labeled.jsonl — one line per issue with {"number": N, "labels": ["label-a", "label-b"]}.
func runLabel(ctx context.Context, client inference.Client, cfg *config.Config, dataDir string) error {
	extractions, err := readJSONL(filepath.Join(dataDir, "extracted.jsonl"))
	if err != nil {
		return err
	}
	slog.Info("label: starting", "issues", len(extractions))

	system := `You normalize proposed fixes into short canonical labels.

You will receive a JSON object with a what_went_wrong diagnosis and potential_fixes list.
Return a JSON object with the issue number and 1-3 labels:

{"number": 1234, "labels": ["label-one", "label-two"]}

Label rules:
- kebab-case, 3-6 words max
- Name the behavioral decision that should change, not the mechanism or feature area
- Two issues that need the same code change MUST get the same label
- Two issues that need different code changes MUST get different labels
- Labels should be reusable across issues. Think: "what label would 20 other similar issues also use?"

Examples of good labels:
- consolidation-savings-threshold
- count-inflight-nodeclaims-against-limits
- surface-disruption-blocking-reason
- drift-ignore-external-mutations
- batch-drift-replacements

Examples of bad labels (too specific to one issue):
- fix-issue-2922-consolidation-loop
- add-min-savings-check-in-consolidation-controller
- bottlerocket-toml-clusterdnsip-array`

	if cfg.DomainContext != "" {
		system += "\n\nDomain context:\n" + cfg.DomainContext
	}

	cwnd := newCwndController(initialCwnd, maxCwnd)
	results := make([]json.RawMessage, len(extractions))
	var totalUsage inference.Usage
	var usageMu sync.Mutex
	var completed int64
	var labelErrors int64

	var wg sync.WaitGroup
	for i, ext := range extractions {
		wg.Add(1)
		go func(idx int, extraction json.RawMessage) {
			defer wg.Done()
			cwnd.acquire()

			text, usage, throttled, err := converseWithRetry(ctx, client, system, string(extraction))
			usageMu.Lock()
			totalUsage = totalUsage.Add(usage)
			usageMu.Unlock()

			if throttled {
				cwnd.onThrottle()
			} else {
				cwnd.onSuccess()
			}

			if err != nil {
				atomic.AddInt64(&labelErrors, 1)
				slog.Error("label: call failed", "index", idx, "error", err)
				return
			}
			results[idx] = json.RawMessage(stripCodeFences(text))
			n := atomic.AddInt64(&completed, 1)
			if n%50 == 0 {
				slog.Info("label: progress", "completed", n, "total", len(extractions))
			}
		}(i, ext)
	}
	wg.Wait()

	slog.Info("label: done",
		"completed", completed, "errors", labelErrors,
		"input_tokens", totalUsage.InputTokens, "output_tokens", totalUsage.OutputTokens,
		"cost", fmt.Sprintf("$%.4f", totalUsage.Cost()))

	if err := writeResults(filepath.Join(dataDir, "labeled.jsonl"), results); err != nil {
		return err
	}
	return validateLabel(dataDir)
}

// buildLabelFrequencyTable reads labeled.jsonl and returns a sorted frequency table
// of label -> issue numbers, plus the raw labeled data.
func buildLabelFrequencyTable(dataDir string) (string, error) {
	lines, err := readJSONL(filepath.Join(dataDir, "labeled.jsonl"))
	if err != nil {
		return "", err
	}

	type labeled struct {
		Number int      `json:"number"`
		Labels []string `json:"labels"`
	}

	labelIssues := make(map[string][]int)
	for _, raw := range lines {
		var l labeled
		if json.Unmarshal(raw, &l) != nil || l.Number == 0 {
			continue
		}
		for _, label := range l.Labels {
			labelIssues[label] = append(labelIssues[label], l.Number)
		}
	}

	// Sort by frequency descending
	type entry struct {
		label  string
		issues []int
	}
	var entries []entry
	for label, issues := range labelIssues {
		entries = append(entries, entry{label, issues})
	}
	sort.Slice(entries, func(i, j int) bool {
		return len(entries[i].issues) > len(entries[j].issues)
	})

	var sb strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&sb, "%s: %d issues %v\n", e.label, len(e.issues), e.issues)
	}
	return sb.String(), nil
}

// runAggregate drafts themes with Opus, then assigns issues with parallel Sonnet calls.
func runAggregate(ctx context.Context, opus, sonnet inference.Client, cfg *config.Config, dataDir, promptsDir string, evalFeedback string) error {
	// Step 1: Draft themes (Opus)
	themes, err := runDraftThemes(ctx, opus, cfg, dataDir, promptsDir, evalFeedback)
	if err != nil {
		return err
	}

	// Step 2: Merge themes that address the same behavioral decision (Opus)
	themes, err = runMergeThemes(ctx, opus, dataDir, themes)
	if err != nil {
		return err
	}

	// Step 3: Assign each issue to themes (parallel Sonnet)
	if err := runAssignIssues(ctx, sonnet, cfg, dataDir, promptsDir, themes); err != nil {
		return err
	}

	return nil
}

func runMergeThemes(ctx context.Context, client inference.Client, dataDir string, themes string) (string, error) {
	themeCount := 0
	for _, line := range strings.Split(themes, "\n") {
		if strings.TrimSpace(line) != "" {
			themeCount++
		}
	}
	slog.Info("merge-themes: starting", "input_themes", themeCount)

	system := `You are reviewing a list of fix themes for redundancy. Your job is to merge
themes that would be resolved by the same code change.

The test: if fixing theme A would also fix most of theme B's issues (or vice
versa), they are one theme. Different root causes that share the same fix are
not separate themes.

Examples of merges:
- "Add minimum savings threshold for consolidation" + "Fix scheduling
  simulation to prevent churn" + "Prevent premature consolidation of new
  nodes" + "Improve multi-node consolidation candidate selection"
  → ONE theme: "Evaluate whether each consolidation move is worth executing"
  (all are fixed by better pre-execution evaluation in the consolidation path)
- "Emit pod-level disruption events" + "Fix metric registration" + "Add
  structured logging" → ONE theme: "Surface decisions and errors as observable
  signals" (all are fixed by adding instrumentation to existing code paths)

Do NOT merge themes where the fixes are genuinely independent — different
code paths, different subsystems, different engineers. "Batch drift
replacements" and "Detect drift by semantic comparison" touch the same
subsystem but require different code changes.

Return the merged theme list as JSONL. Preserve all fields: theme_id, title,
description, theme_type, severity, labels. For merged themes, pick the broadest
title, combine descriptions, use the highest severity, and union the labels
arrays. Update theme_id.`

	user := "Here are the themes to review:\n\n```jsonl\n" + themes + "\n```\n\n" +
		"Merge themes that address the same behavioral decision. Return the result as ```jsonl ... ```."

	text, usage, err := inference.Converse(ctx, client, system, user,
		inference.WithMaxTokens(16384))
	if err != nil {
		return "", fmt.Errorf("merge-themes call failed: %w", err)
	}
	slog.Info("merge-themes: done",
		"input_tokens", usage.InputTokens, "output_tokens", usage.OutputTokens,
		"cost", fmt.Sprintf("$%.4f", usage.Cost()))

	merged := stripCodeFences(extractFencedBlock(text, "jsonl"))
	if merged == "" {
		merged = stripCodeFences(text)
	}

	mergedCount := 0
	for _, line := range strings.Split(merged, "\n") {
		if strings.TrimSpace(line) != "" {
			mergedCount++
		}
	}
	slog.Info("merge-themes: result", "output_themes", mergedCount, "merged", themeCount-mergedCount)

	os.WriteFile(filepath.Join(dataDir, "draft-themes.jsonl"), []byte(merged), 0644)
	return merged, nil
}

// runDraftThemes asks Opus to cluster fix labels into themes using the label frequency table.
func runDraftThemes(ctx context.Context, client inference.Client, cfg *config.Config, dataDir, promptsDir string, evalFeedback string) (string, error) {
	labelTable, err := buildLabelFrequencyTable(dataDir)
	if err != nil {
		return "", fmt.Errorf("building label frequency table: %w", err)
	}
	issueCount := countLines(filepath.Join(dataDir, "extracted.jsonl"))
	slog.Info("draft-themes: starting", "issues", issueCount)

	system, err := renderPrompt(filepath.Join(promptsDir, "aggregate.md"), templateData{
		Project:       cfg.Project,
		DomainContext: cfg.DomainContext,
		IssueCount:    issueCount,
	})
	if err != nil {
		return "", fmt.Errorf("rendering aggregate prompt: %w", err)
	}

	var userBuilder strings.Builder
	userBuilder.WriteString("Here is the fix label frequency table. Each line is a label that issues proposed, the number of issues that used it, and their issue numbers.\n\n")
	userBuilder.WriteString(labelTable)
	if evalFeedback != "" {
		userBuilder.WriteString("\n\n## Evaluation Feedback from Previous Round\n\n")
		userBuilder.WriteString(evalFeedback)
		userBuilder.WriteString("\n\nUse this feedback to improve the theme definitions. ")
		userBuilder.WriteString("Merge themes that overlap. Split themes that are too broad. ")
		userBuilder.WriteString("Create new themes for unaddressed issues if a pattern emerges.\n")
	}
	userBuilder.WriteString("\n\nCluster these labels into fix themes. Labels that address the same behavioral decision should be merged into one theme. Labels that address different decisions must stay separate.\n\n")
	userBuilder.WriteString("For each theme, return:\n")
	userBuilder.WriteString("- theme_id (kebab-case, named after the decision that changes)\n")
	userBuilder.WriteString("- title (imperative sentence: what the system should do differently)\n")
	userBuilder.WriteString("- description (1-2 sentences: what behavioral change this represents)\n")
	userBuilder.WriteString("- theme_type (behavioral_change / feature_surface / infrastructure)\n")
	userBuilder.WriteString("- severity (high / medium / low)\n")
	userBuilder.WriteString("- labels (array of the input labels that belong to this theme)\n\n")
	userBuilder.WriteString("The label counts are hard evidence for theme size. A theme that merges labels totaling 45 issues is a big theme. Do not split it into sub-themes unless the labels genuinely require different code changes.\n")
	userBuilder.WriteString("Aim for 40-55 themes. Return a JSONL block wrapped in ```jsonl ... ```.")
	user := userBuilder.String()

	text, usage, err := inference.Converse(ctx, client, system, user,
		inference.WithMaxTokens(65536))
	if err != nil {
		return "", fmt.Errorf("draft-themes call failed: %w", err)
	}
	slog.Info("draft-themes: done",
		"input_tokens", usage.InputTokens, "output_tokens", usage.OutputTokens,
		"cost", fmt.Sprintf("$%.4f", usage.Cost()))

	themes := stripCodeFences(extractFencedBlock(text, "jsonl"))
	if themes == "" {
		themes = stripCodeFences(text)
	}

	// Save draft themes for debugging
	os.WriteFile(filepath.Join(dataDir, "draft-themes.jsonl"), []byte(themes), 0644)

	themeCount := 0
	for _, line := range strings.Split(themes, "\n") {
		if strings.TrimSpace(line) != "" {
			themeCount++
		}
	}
	slog.Info("draft-themes: produced", "themes", themeCount)

	return themes, nil
}

// assignment is the per-issue result from runAssignIssues.
type assignment struct {
	Number    int      `json:"number"`
	ThemeIDs  []string `json:"theme_ids"`
	Reasoning string   `json:"reasoning"`
}

// runAssignIssues assigns each issue to draft themes via parallel Sonnet calls,
// then assembles fix-themes.jsonl and fix-priority.md.
func runAssignIssues(ctx context.Context, client inference.Client, cfg *config.Config, dataDir, promptsDir string, themes string) error {
	extractions, err := readJSONL(filepath.Join(dataDir, "extracted.jsonl"))
	if err != nil {
		return err
	}
	slog.Info("assign-issues: starting", "issues", len(extractions), "themes_context_bytes", len(themes))

	system := "You are assigning GitHub issues to fix themes.\n\n" +
		"You will receive a list of fix theme IDs with titles, and one issue extraction.\n" +
		"Return ONLY a JSON object with no other text:\n" +
		"```json\n" +
		"{\"number\": 1234, \"theme_ids\": [\"theme-id-1\"], \"reasoning\": \"brief explanation\"}\n" +
		"```\n\n" +
		"Only assign a theme if its fix DIRECTLY addresses the root cause described in what_went_wrong.\n" +
		"Do not assign a theme just because it is tangentially related.\n" +
		"Most issues should match 1-2 themes. Many will match 0.\n" +
		"If no theme fits, return an empty theme_ids array."

	// Build compact theme context: just IDs + titles, not full descriptions
	themeContext := buildCompactThemeContext(themes)

	cwnd := newCwndController(initialCwnd, maxCwnd)
	results := make([]json.RawMessage, len(extractions))
	var totalUsage inference.Usage
	var usageMu sync.Mutex
	var completed int64
	var errors int64

	var wg sync.WaitGroup
	for i, ext := range extractions {
		wg.Add(1)
		go func(idx int, extraction json.RawMessage) {
			defer wg.Done()
			cwnd.acquire()

			user := themeContext + "Issue extraction:\n" + string(extraction)

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
				slog.Error("assign-issues: call failed", "index", idx, "error", err)
				return
			}
			results[idx] = json.RawMessage(stripCodeFences(text))
			n := atomic.AddInt64(&completed, 1)
			if n%50 == 0 {
				slog.Info("assign-issues: progress", "completed", n, "total", len(extractions))
			}
		}(i, ext)
	}
	wg.Wait()

	slog.Info("assign-issues: done",
		"completed", completed, "errors", errors,
		"input_tokens", totalUsage.InputTokens, "output_tokens", totalUsage.OutputTokens,
		"cost", fmt.Sprintf("$%.4f", totalUsage.Cost()))

	// Assemble fix-themes.jsonl from draft themes + assignments
	return assembleThemes(dataDir, themes, results)
}

// buildCompactThemeContext extracts just theme_id and title from draft themes JSONL.
func buildCompactThemeContext(themes string) string {
	var sb strings.Builder
	sb.WriteString("Fix themes:\n")
	for _, line := range strings.Split(themes, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var t struct {
			ThemeID string `json:"theme_id"`
			Title   string `json:"title"`
		}
		if json.Unmarshal([]byte(line), &t) == nil && t.ThemeID != "" {
			fmt.Fprintf(&sb, "- %s: %s\n", t.ThemeID, t.Title)
		}
	}
	sb.WriteString("\n")
	return sb.String()
}

// assembleThemes combines draft theme definitions with per-issue assignments
// into fix-themes.jsonl and fix-priority.md. Reads extracted.jsonl to populate
// sample_what_went_wrong per theme.
func assembleThemes(dataDir string, draftThemes string, assignments []json.RawMessage) error {
	// Load extractions for sample_what_went_wrong.
	extractionsByNumber := make(map[int]string)
	if extLines, err := readJSONL(filepath.Join(dataDir, "extracted.jsonl")); err == nil {
		for _, raw := range extLines {
			var e struct {
				Number        int    `json:"number"`
				WhatWentWrong string `json:"what_went_wrong"`
			}
			if json.Unmarshal(raw, &e) == nil && e.Number != 0 {
				extractionsByNumber[e.Number] = e.WhatWentWrong
			}
		}
	}
	// Parse draft themes
	type draftTheme struct {
		ThemeID     string `json:"theme_id"`
		Title       string `json:"title"`
		Description string `json:"description"`
		ThemeType   string `json:"theme_type"`
		Severity    string `json:"severity"`
	}

	themesByID := make(map[string]*draftTheme)
	var themeOrder []string
	for _, line := range strings.Split(draftThemes, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var t draftTheme
		if json.Unmarshal([]byte(line), &t) == nil && t.ThemeID != "" {
			themesByID[t.ThemeID] = &t
			themeOrder = append(themeOrder, t.ThemeID)
		}
	}

	// Collect issue assignments per theme
	themeIssues := make(map[string][]int)
	for _, raw := range assignments {
		if raw == nil {
			continue
		}
		var compact bytes.Buffer
		if json.Compact(&compact, raw) != nil {
			continue
		}
		var a assignment
		if json.Unmarshal(compact.Bytes(), &a) != nil {
			continue
		}
		for _, tid := range a.ThemeIDs {
			themeIssues[tid] = append(themeIssues[tid], a.Number)
		}
	}

	// Severity weights for scoring
	severityWeight := map[string]float64{"high": 3.0, "medium": 1.0, "low": 0.5}

	// Build fix-themes.jsonl
	type fullTheme struct {
		ThemeID            string   `json:"theme_id"`
		Title              string   `json:"title"`
		Description        string   `json:"description"`
		ThemeType          string   `json:"theme_type"`
		Severity           string   `json:"severity"`
		IssueNumbers       []int    `json:"issue_numbers"`
		IssueCount         int      `json:"issue_count"`
		Score              float64  `json:"score"`
		SampleWhatWentWrong []string `json:"sample_what_went_wrong,omitempty"`
	}

	var themesOut []fullTheme
	for _, tid := range themeOrder {
		dt := themesByID[tid]
		issues := themeIssues[tid]
		if len(issues) == 0 {
			continue
		}
		sw := severityWeight[dt.Severity]
		if sw == 0 {
			sw = 1.0
		}
		score := sw * float64(len(issues))
		// Pick first two sample diagnoses by issue number order.
		var samples []string
		for _, num := range issues {
			if diag, ok := extractionsByNumber[num]; ok && diag != "" {
				samples = append(samples, fmt.Sprintf("Issue #%d: %s", num, diag))
				if len(samples) >= 2 {
					break
				}
			}
		}
		themesOut = append(themesOut, fullTheme{
			ThemeID:             dt.ThemeID,
			Title:               dt.Title,
			Description:         dt.Description,
			ThemeType:           dt.ThemeType,
			Severity:            dt.Severity,
			IssueNumbers:        issues,
			IssueCount:          len(issues),
			Score:               score,
			SampleWhatWentWrong: samples,
		})
	}

	// Sort by score descending
	sort.Slice(themesOut, func(i, j int) bool {
		return themesOut[i].Score > themesOut[j].Score
	})

	// Write fix-themes.jsonl
	f, err := os.Create(filepath.Join(dataDir, "fix-themes.jsonl"))
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	for _, t := range themesOut {
		enc.Encode(t)
	}
	f.Close()

	// Write fix-priority.md
	var md strings.Builder
	md.WriteString("# Fix Priority\n\n## Coverage Summary\n\n")
	md.WriteString("| Rank | Fix | Issues | Severity | Score |\n")
	md.WriteString("|------|-----|--------|----------|-------|\n")
	for i, t := range themesOut {
		fmt.Fprintf(&md, "| %d | %s | %d | %s | %.1f |\n",
			i+1, t.Title, t.IssueCount, t.Severity, t.Score)
	}

	allIssues := make(map[int]bool)
	for _, t := range themesOut {
		for _, n := range t.IssueNumbers {
			allIssues[n] = true
		}
	}
	total := countLines(filepath.Join(dataDir, "issues.jsonl"))

	top3Issues := make(map[int]bool)
	top5Issues := make(map[int]bool)
	top10Issues := make(map[int]bool)
	for i, t := range themesOut {
		for _, n := range t.IssueNumbers {
			if i < 3 { top3Issues[n] = true }
			if i < 5 { top5Issues[n] = true }
			if i < 10 { top10Issues[n] = true }
		}
	}

	fmt.Fprintf(&md, "\nThe top 3 fixes cover %d of %d issues (%d%%).\n", len(top3Issues), total, len(top3Issues)*100/max(total, 1))
	fmt.Fprintf(&md, "The top 5 fixes cover %d of %d issues (%d%%).\n", len(top5Issues), total, len(top5Issues)*100/max(total, 1))
	fmt.Fprintf(&md, "The top 10 fixes cover %d of %d issues (%d%%).\n", len(top10Issues), total, len(top10Issues)*100/max(total, 1))

	md.WriteString("\n## Fix Details\n")
	for i, t := range themesOut {
		fmt.Fprintf(&md, "\n### %d. %s (%d issues)\n\n%s\n\nType: %s | Severity: %s | Score: %.1f\n",
			i+1, t.Title, t.IssueCount, t.Description, t.ThemeType, t.Severity, t.Score)
	}

	if err := os.WriteFile(filepath.Join(dataDir, "fix-priority.md"), []byte(md.String()), 0644); err != nil {
		return err
	}
	return validateAggregate(dataDir)
}

// runEvaluate calls Sonnet once per issue with slow-start and per-issue theme filtering.
func runEvaluate(ctx context.Context, client inference.Client, cfg *config.Config, dataDir, promptsDir string) error {
	issues, err := readJSONL(filepath.Join(dataDir, "issues.jsonl"))
	if err != nil {
		return err
	}
	extractions, err := readJSONL(filepath.Join(dataDir, "extracted.jsonl"))
	if err != nil {
		return err
	}
	themeLines, err := readJSONL(filepath.Join(dataDir, "fix-themes.jsonl"))
	if err != nil {
		return err
	}

	// Build reverse index: issue number -> relevant theme lines
	issueToThemes := buildThemeIndex(themeLines, issues)
	slog.Info("evaluate: starting", "issues", len(issues), "themes", len(themeLines))

	system, err := renderPrompt(filepath.Join(promptsDir, "evaluate.md"), templateData{
		Project:       cfg.Project,
		DomainContext: cfg.DomainContext,
		IssueCount:    len(issues),
	})
	if err != nil {
		return fmt.Errorf("rendering evaluate prompt: %w", err)
	}

	cwnd := newCwndController(initialCwnd, maxCwnd)
	results := make([]json.RawMessage, len(issues))
	var totalUsage inference.Usage
	var usageMu sync.Mutex
	var completed int64
	var errors int64

	var wg sync.WaitGroup
	for i := range issues {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			cwnd.acquire()

			var extraction string
			if idx < len(extractions) {
				extraction = string(extractions[idx])
			}

			// Only include themes that claim this issue
			relevantThemes := issueToThemes[idx]
			var themeContext string
			if len(relevantThemes) > 0 {
				var sb strings.Builder
				sb.WriteString("Fix themes that claim to cover this issue:\n")
				for _, t := range relevantThemes {
					sb.Write(t)
					sb.WriteByte('\n')
				}
				themeContext = sb.String()
			} else {
				themeContext = "No fix themes claim to cover this issue.\n"
			}

			user := themeContext + "\n" +
				"Issue:\n" + string(issues[idx]) + "\n\n" +
				"Extraction:\n" + extraction + "\n\n" +
				"Evaluate which fix themes apply to this issue. Return a single JSON object."

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
				slog.Error("evaluate: call failed", "index", idx, "error", err)
				return
			}
			results[idx] = json.RawMessage(stripCodeFences(text))
			n := atomic.AddInt64(&completed, 1)
			if n%50 == 0 {
				slog.Info("evaluate: progress", "completed", n, "total", len(issues))
			}
		}(i)
	}
	wg.Wait()

	slog.Info("evaluate: done",
		"completed", completed, "errors", errors,
		"input_tokens", totalUsage.InputTokens, "output_tokens", totalUsage.OutputTokens,
		"cost", fmt.Sprintf("$%.4f", totalUsage.Cost()))

	if err := writeResults(filepath.Join(dataDir, "evaluated.jsonl"), results); err != nil {
		return err
	}
	if err := writeEvalSummary(dataDir); err != nil {
		return err
	}
	return validateEvaluate(dataDir)
}

// buildThemeIndex creates a reverse index from issue position to relevant theme JSON lines.
func buildThemeIndex(themeLines []json.RawMessage, issues []json.RawMessage) map[int][]json.RawMessage {
	// Extract issue numbers from issues to map position -> number
	type issueHeader struct {
		Number int `json:"number"`
	}
	numToIdx := make(map[int]int)
	for i, raw := range issues {
		var h issueHeader
		json.Unmarshal(raw, &h)
		numToIdx[h.Number] = i
	}

	// For each theme, map its issue_numbers to positions
	type themeHeader struct {
		IssueNumbers []int `json:"issue_numbers"`
	}
	result := make(map[int][]json.RawMessage)
	for _, raw := range themeLines {
		var h themeHeader
		json.Unmarshal(raw, &h)
		for _, num := range h.IssueNumbers {
			if idx, ok := numToIdx[num]; ok {
				result[idx] = append(result[idx], raw)
			}
		}
	}
	return result
}

// evalStats summarizes evaluation results.
type evalStats struct {
	yes         int
	partial     int
	no          int
	unaddressed int
	total       int
}

func (s evalStats) misattributionRate() float64 {
	verdicts := s.yes + s.partial + s.no
	if verdicts == 0 {
		return 0
	}
	return float64(s.no) / float64(verdicts)
}

// computeEvalStats reads evaluated.jsonl and computes verdict counts.
func computeEvalStats(path string) evalStats {
	var s evalStats
	lines, err := readJSONL(path)
	if err != nil {
		return s
	}
	for _, line := range lines {
		var eval struct {
			ApplicableFixes []struct {
				Verdict string `json:"verdict"`
			} `json:"applicable_fixes"`
			Unaddressed bool `json:"unaddressed"`
		}
		if json.Unmarshal(line, &eval) != nil {
			continue
		}
		s.total++
		if eval.Unaddressed {
			s.unaddressed++
		}
		for _, f := range eval.ApplicableFixes {
			switch f.Verdict {
			case "yes":
				s.yes++
			case "partial":
				s.partial++
			case "no":
				s.no++
			}
		}
	}
	return s
}

// summarizeEvalFeedback reads evaluated.jsonl and produces a text summary
// of misattributions and unaddressed issues for the re-aggregate step.
func summarizeEvalFeedback(path string) string {
	lines, err := readJSONL(path)
	if err != nil {
		return ""
	}

	var misattributed []string
	var unaddressed []string

	for _, line := range lines {
		var eval struct {
			Number          int    `json:"number"`
			Title           string `json:"title"`
			ApplicableFixes []struct {
				ThemeID   string `json:"theme_id"`
				Verdict   string `json:"verdict"`
				Reasoning string `json:"reasoning"`
			} `json:"applicable_fixes"`
			Unaddressed       bool   `json:"unaddressed"`
			UnaddressedReason string `json:"unaddressed_reason"`
		}
		if json.Unmarshal(line, &eval) != nil {
			continue
		}

		for _, f := range eval.ApplicableFixes {
			if f.Verdict == "no" {
				misattributed = append(misattributed,
					fmt.Sprintf("- Issue #%d (%s) was wrongly assigned to theme %q: %s",
						eval.Number, eval.Title, f.ThemeID, f.Reasoning))
			}
		}
		if eval.Unaddressed {
			reason := eval.UnaddressedReason
			if reason == "" {
				reason = "no theme covers this issue"
			}
			unaddressed = append(unaddressed,
				fmt.Sprintf("- Issue #%d (%s): %s", eval.Number, eval.Title, reason))
		}
	}

	var sb strings.Builder
	if len(misattributed) > 0 {
		fmt.Fprintf(&sb, "### Misattributed issues (%d)\n\n", len(misattributed))
		// Cap to avoid blowing context
		limit := min(len(misattributed), 50)
		for _, m := range misattributed[:limit] {
			sb.WriteString(m)
			sb.WriteByte('\n')
		}
		if len(misattributed) > limit {
			fmt.Fprintf(&sb, "- ...and %d more\n", len(misattributed)-limit)
		}
		sb.WriteByte('\n')
	}
	if len(unaddressed) > 0 {
		fmt.Fprintf(&sb, "### Unaddressed issues (%d)\n\n", len(unaddressed))
		limit := min(len(unaddressed), 50)
		for _, u := range unaddressed[:limit] {
			sb.WriteString(u)
			sb.WriteByte('\n')
		}
		if len(unaddressed) > limit {
			fmt.Fprintf(&sb, "- ...and %d more\n", len(unaddressed)-limit)
		}
	}
	return sb.String()
}

// converseWithRetry calls the LLM with app-level retry on throttle errors.
// Returns (text, cumulative usage, whether any attempt was throttled, error).
func converseWithRetry(ctx context.Context, client inference.Client, system, user string) (string, inference.Usage, bool, error) {
	var cumUsage inference.Usage
	throttled := false
	for attempt := range maxRetries {
		text, usage, err := inference.Converse(ctx, client, system, user,
			inference.WithMaxTokens(4096))
		cumUsage = cumUsage.Add(usage)
		if err == nil {
			return text, cumUsage, throttled, nil
		}
		if !isThrottleError(err) {
			return text, cumUsage, throttled, err
		}
		throttled = true
		backoff := time.Duration(1<<uint(attempt)) * 2 * time.Second
		slog.Warn("app retry", "attempt", attempt+1, "backoff", backoff)
		select {
		case <-ctx.Done():
			return "", cumUsage, throttled, ctx.Err()
		case <-time.After(backoff):
		}
	}
	return "", cumUsage, throttled, fmt.Errorf("failed after %d retries", maxRetries)
}

func isThrottleError(err error) bool {
	s := err.Error()
	return strings.Contains(s, "ThrottlingException") || strings.Contains(s, "429") || strings.Contains(s, "Too many tokens")
}

// helpers

// stripCodeFences removes ```json ... ``` wrapping from LLM responses.
func stripCodeFences(s string) string {
	s = strings.TrimSpace(s)
	for _, prefix := range []string{"```json", "```JSON", "```"} {
		if strings.HasPrefix(s, prefix) {
			s = s[len(prefix):]
			if idx := strings.LastIndex(s, "```"); idx >= 0 {
				s = s[:idx]
			}
			return strings.TrimSpace(s)
		}
	}
	return s
}

func readJSONL(path string) ([]json.RawMessage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var items []json.RawMessage
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		cp := make([]byte, len(line))
		copy(cp, line)
		items = append(items, json.RawMessage(cp))
	}
	return items, scanner.Err()
}

func writeResults(path string, results []json.RawMessage) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, r := range results {
		if r == nil {
			f.WriteString("{}\n")
			continue
		}
		// Compact multi-line JSON into single-line JSONL
		var compact bytes.Buffer
		if err := json.Compact(&compact, r); err != nil {
			// Not valid JSON — write as-is
			f.Write(r)
		} else {
			f.Write(compact.Bytes())
		}
		f.WriteString("\n")
	}
	return nil
}

func countLines(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	count := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		if len(scanner.Bytes()) > 0 {
			count++
		}
	}
	return count
}

func extractFencedBlock(text, lang string) string {
	fence := "```" + lang
	start := strings.Index(text, fence)
	if start < 0 {
		return ""
	}
	content := text[start+len(fence):]
	nl := strings.Index(content, "\n")
	if nl < 0 {
		return ""
	}
	content = content[nl+1:]
	end := strings.Index(content, "```")
	if end < 0 {
		return ""
	}
	return content[:end]
}
