package pipeline

import (
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
	"time"

	"github.com/jamesmt/vibish-triage/internal/bedrock"
	"github.com/jamesmt/vibish-triage/internal/config"
	"github.com/jamesmt/vibish-triage/internal/download"
	"github.com/jamesmt/vibish-triage/internal/inference"
)

// Report downloads current issues, diffs against the last full run,
// diagnoses new issues, maps them to existing themes, and writes a report.
func Report(ctx context.Context, cfg *config.Config, dataDir string, timeout time.Duration, workers int) error {
	// Check that themes exist from a prior run.
	themesPath := filepath.Join(dataDir, "fix-themes.jsonl")
	if _, err := os.Stat(themesPath); err != nil {
		return fmt.Errorf("no themes found at %s — run the full pipeline first", themesPath)
	}

	// Load known issue numbers from last run.
	knownIssues := make(map[int]bool)
	if lines, err := readJSONL(filepath.Join(dataDir, "issues.jsonl")); err == nil {
		for _, raw := range lines {
			var h struct{ Number int `json:"number"` }
			json.Unmarshal(raw, &h)
			if h.Number != 0 {
				knownIssues[h.Number] = true
			}
		}
	}
	slog.Info("report: loaded known issues", "count", len(knownIssues))

	// Download current issues to a temp file.
	tmpDir, err := os.MkdirTemp("", "vibish-report-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	cacheDir := filepath.Join(dataDir, ".cache")
	if err := download.Run(cfg.Repos, cfg.State, tmpDir, cacheDir, workers); err != nil {
		return fmt.Errorf("download failed: %w", err)
	}

	// Find new issues.
	currentLines, err := readJSONL(filepath.Join(tmpDir, "issues.jsonl"))
	if err != nil {
		return fmt.Errorf("reading downloaded issues: %w", err)
	}

	var newIssues []json.RawMessage
	var newHeaders []issueHeader
	for _, raw := range currentLines {
		var h issueHeader
		if json.Unmarshal(raw, &h) != nil || h.Number == 0 {
			continue
		}
		if !knownIssues[h.Number] {
			newIssues = append(newIssues, raw)
			newHeaders = append(newHeaders, h)
		}
	}

	if len(newIssues) == 0 {
		slog.Info("report: no new issues since last run")
		return nil
	}
	slog.Info("report: new issues found", "count", len(newIssues))

	// Initialize Sonnet.
	sonnet, err := bedrock.NewClient(ctx, "claude-sonnet")
	if err != nil {
		return fmt.Errorf("creating sonnet client: %w", err)
	}

	// Extract: diagnose each new issue.
	slog.Info("report: extracting", "issues", len(newIssues))
	extractSystem := buildExtractSystem(cfg)
	extractions, extractUsage, err := parallelConverse(ctx, sonnet, extractSystem, newIssues, "extract")
	if err != nil {
		return err
	}

	// Load existing themes for assignment.
	themeLines, err := readJSONL(themesPath)
	if err != nil {
		return err
	}
	themeContext := buildCompactThemeContext(themeLinesToString(themeLines))

	// Assign: map each issue to themes.
	slog.Info("report: assigning", "issues", len(extractions))
	assignSystem := "You are assigning GitHub issues to fix themes.\n\n" +
		"You will receive a list of fix theme IDs with titles, and one issue extraction.\n" +
		"Return ONLY a JSON object with no other text:\n" +
		"```json\n" +
		"{\"number\": 1234, \"theme_ids\": [\"theme-id-1\"], \"reasoning\": \"brief explanation\"}\n" +
		"```\n\n" +
		"Only assign a theme if its fix DIRECTLY addresses the root cause described in what_went_wrong.\n" +
		"Do not assign a theme just because it is tangentially related.\n" +
		"Most issues should match 1-2 themes. Many will match 0.\n" +
		"If no theme fits, return an empty theme_ids array."

	var assignInputs []json.RawMessage
	for _, ext := range extractions {
		user := themeContext + "Issue extraction:\n" + string(ext)
		assignInputs = append(assignInputs, json.RawMessage(user))
	}
	assignments, assignUsage, err := parallelConverseRaw(ctx, sonnet, assignSystem, assignInputs, "assign")
	if err != nil {
		return err
	}

	totalUsage := extractUsage.Add(assignUsage)
	slog.Info("report: done",
		"input_tokens", totalUsage.InputTokens, "output_tokens", totalUsage.OutputTokens,
		"cost", fmt.Sprintf("$%.4f", totalUsage.Cost()))

	// Build theme lookup.
	type themeInfo struct {
		ThemeID    string `json:"theme_id"`
		Title      string `json:"title"`
		Severity   string `json:"severity"`
		IssueCount int    `json:"issue_count"`
		Score      float64 `json:"score"`
	}
	themesByID := make(map[string]*themeInfo)
	var themeOrder []string
	for _, raw := range themeLines {
		var t themeInfo
		if json.Unmarshal(raw, &t) == nil && t.ThemeID != "" {
			themesByID[t.ThemeID] = &t
			themeOrder = append(themeOrder, t.ThemeID)
		}
	}

	// Build rank lookup.
	themeRank := make(map[string]int)
	for i, tid := range themeOrder {
		themeRank[tid] = i + 1
	}

	// Parse assignments.
	type issueRec struct {
		number     int
		repo       string
		title      string
		created    string
		diagnosis  string
		fixes      []string
		themeIDs   []string
	}

	var recs []issueRec
	for i, hdr := range newHeaders {
		var ext struct {
			WhatWentWrong  string   `json:"what_went_wrong"`
			PotentialFixes []string `json:"potential_fixes"`
		}
		if i < len(extractions) {
			json.Unmarshal(extractions[i], &ext)
		}

		var a assignment
		if i < len(assignments) {
			json.Unmarshal(assignments[i], &a)
		}

		recs = append(recs, issueRec{
			number:    hdr.Number,
			repo:      hdr.Repo,
			title:     hdr.Title,
			created:   hdr.CreatedAt,
			diagnosis: ext.WhatWentWrong,
			fixes:     ext.PotentialFixes,
			themeIDs:  a.ThemeIDs,
		})
	}

	// Group by theme.
	type themeGroup struct {
		theme   *themeInfo
		rank    int
		issues  []issueRec
	}
	groupMap := make(map[string]*themeGroup)
	var ungrouped []issueRec

	for _, rec := range recs {
		if len(rec.themeIDs) == 0 {
			ungrouped = append(ungrouped, rec)
			continue
		}
		for _, tid := range rec.themeIDs {
			g, ok := groupMap[tid]
			if !ok {
				t := themesByID[tid]
				if t == nil {
					continue
				}
				g = &themeGroup{theme: t, rank: themeRank[tid]}
				groupMap[tid] = g
			}
			g.issues = append(g.issues, rec)
		}
	}

	var groups []*themeGroup
	for _, g := range groupMap {
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].rank < groups[j].rank
	})

	// Write report.
	var md strings.Builder
	today := time.Now().Format("2006-01-02")
	fmt.Fprintf(&md, "# New Issues Report: %s\n\n", today)
	fmt.Fprintf(&md, "%d new issues since last full run. Mapped to %d existing themes.\n\n",
		len(recs), len(groups))

	// Summary table.
	md.WriteString("## Summary\n\n")
	md.WriteString("| Theme | Rank | New | Total | Severity | Issues |\n")
	md.WriteString("|-------|------|-----|-------|----------|--------|\n")
	for _, g := range groups {
		var nums []string
		for _, rec := range g.issues {
			nums = append(nums, fmt.Sprintf("#%d", rec.number))
		}
		fmt.Fprintf(&md, "| %s | %d | %d | %d | %s | %s |\n",
			g.theme.Title, g.rank, len(g.issues), g.theme.IssueCount,
			g.theme.Severity, strings.Join(nums, ", "))
	}
	if len(ungrouped) > 0 {
		var nums []string
		for _, rec := range ungrouped {
			nums = append(nums, fmt.Sprintf("#%d", rec.number))
		}
		fmt.Fprintf(&md, "| *(no theme)* | — | %d | — | — | %s |\n",
			len(ungrouped), strings.Join(nums, ", "))
	}

	// Per-issue details.
	md.WriteString("\n## Details\n")
	for _, rec := range recs {
		fmt.Fprintf(&md, "\n### %s#%d: %s\n\n", rec.repo, rec.number, rec.title)
		fmt.Fprintf(&md, "**Opened:** %s\n\n", rec.created[:10])
		fmt.Fprintf(&md, "**Diagnosis:** %s\n\n", rec.diagnosis)
		if len(rec.fixes) > 0 {
			md.WriteString("**Proposed fixes:**\n")
			for _, f := range rec.fixes {
				fmt.Fprintf(&md, "- %s\n", f)
			}
			md.WriteString("\n")
		}
		if len(rec.themeIDs) > 0 {
			md.WriteString("**Themes:**\n")
			for _, tid := range rec.themeIDs {
				t := themesByID[tid]
				if t != nil {
					fmt.Fprintf(&md, "- #%d %s (%d total issues, score %.0f)\n",
						themeRank[tid], t.Title, t.IssueCount, t.Score)
				}
			}
		} else {
			md.WriteString("**No existing theme matches this issue.**\n")
		}
		md.WriteString("\n")
	}

	reportPath := filepath.Join(dataDir, fmt.Sprintf("report-%s.md", today))
	if err := os.WriteFile(reportPath, []byte(md.String()), 0644); err != nil {
		return err
	}
	slog.Info("report: written", "path", reportPath, "issues", len(recs))
	return nil
}

type issueHeader struct {
	Number    int    `json:"number"`
	Repo      string `json:"repo"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
}

func buildExtractSystem(cfg *config.Config) string {
	s := `You diagnose GitHub issues. For each issue, identify what went wrong and propose 1-3 fixes.
Return ONLY a JSON object:
{"repo": "org/repo", "number": 1234, "title": "...", "what_went_wrong": "...", "potential_fixes": ["...", "..."]}
Copy repo, number, and title from the input. Write what_went_wrong and potential_fixes.`
	if cfg.DomainContext != "" {
		s += "\n\nDomain context:\n" + cfg.DomainContext
	}
	return s
}

func themeLinesToString(lines []json.RawMessage) string {
	var sb strings.Builder
	for _, l := range lines {
		sb.Write(l)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// parallelConverse runs Sonnet on each input with slow-start, returning results.
func parallelConverse(ctx context.Context, client inference.Client, system string, inputs []json.RawMessage, name string) ([]json.RawMessage, inference.Usage, error) {
	cwnd := newCwndController(initialCwnd, maxCwnd)
	results := make([]json.RawMessage, len(inputs))
	var totalUsage inference.Usage
	var usageMu sync.Mutex
	var completed int64
	var errors int64

	var wg sync.WaitGroup
	for i, input := range inputs {
		wg.Add(1)
		go func(idx int, data json.RawMessage) {
			defer wg.Done()
			cwnd.acquire()

			text, usage, throttled, err := converseWithRetry(ctx, client, system, string(data))
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
				slog.Error(name+": call failed", "index", idx, "error", err)
				return
			}
			results[idx] = json.RawMessage(stripCodeFences(text))
			atomic.AddInt64(&completed, 1)
		}(i, input)
	}
	wg.Wait()

	slog.Info(name+": done", "completed", completed, "errors", errors)
	return results, totalUsage, nil
}

// parallelConverseRaw is like parallelConverse but takes string inputs (already formatted user messages).
func parallelConverseRaw(ctx context.Context, client inference.Client, system string, inputs []json.RawMessage, name string) ([]json.RawMessage, inference.Usage, error) {
	cwnd := newCwndController(initialCwnd, maxCwnd)
	results := make([]json.RawMessage, len(inputs))
	var totalUsage inference.Usage
	var usageMu sync.Mutex
	var completed int64
	var errors int64

	var wg sync.WaitGroup
	for i, input := range inputs {
		wg.Add(1)
		go func(idx int, user string) {
			defer wg.Done()
			cwnd.acquire()

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
				slog.Error(name+": call failed", "index", idx, "error", err)
				return
			}
			results[idx] = json.RawMessage(stripCodeFences(text))
			atomic.AddInt64(&completed, 1)
		}(i, string(input))
	}
	wg.Wait()

	slog.Info(name+": done", "completed", completed, "errors", errors)
	return results, totalUsage, nil
}
