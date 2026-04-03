package pipeline

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// validateDownload checks issues.jsonl per 01-download.md.
func validateDownload(dataDir string) error {
	lines, err := readJSONL(filepath.Join(dataDir, "issues.jsonl"))
	if err != nil {
		return fmt.Errorf("reading issues.jsonl: %w", err)
	}
	if len(lines) == 0 {
		return fmt.Errorf("issues.jsonl is empty")
	}
	for i, raw := range lines {
		var issue struct {
			Repo      string `json:"repo"`
			Number    int    `json:"number"`
			Title     string `json:"title"`
			Body      string `json:"body"`
			UpdatedAt string `json:"updated_at"`
		}
		if err := json.Unmarshal(raw, &issue); err != nil {
			return fmt.Errorf("line %d: invalid JSON: %w", i, err)
		}
		if issue.Repo == "" || issue.Number == 0 || issue.Title == "" || issue.UpdatedAt == "" {
			return fmt.Errorf("line %d: missing required field (repo=%q number=%d title=%q updated_at=%q)",
				i, issue.Repo, issue.Number, issue.Title, issue.UpdatedAt)
		}
	}
	slog.Info("validate: download OK", "issues", len(lines))
	return nil
}

// validateExtract checks extracted.jsonl per 02-extract.md.
func validateExtract(dataDir string) error {
	issueCount := countLines(filepath.Join(dataDir, "issues.jsonl"))
	lines, err := readJSONL(filepath.Join(dataDir, "extracted.jsonl"))
	if err != nil {
		return fmt.Errorf("reading extracted.jsonl: %w", err)
	}
	if len(lines) != issueCount {
		return fmt.Errorf("extracted.jsonl has %d lines, issues.jsonl has %d", len(lines), issueCount)
	}
	for i, raw := range lines {
		var ext struct {
			Number        int      `json:"number"`
			WhatWentWrong string   `json:"what_went_wrong"`
			PotentialFixes []string `json:"potential_fixes"`
		}
		if err := json.Unmarshal(raw, &ext); err != nil {
			continue // empty results from failed calls are {}
		}
		if ext.Number == 0 {
			continue
		}
		if len(ext.PotentialFixes) == 0 {
			slog.Warn("validate: issue has no fixes", "number", ext.Number, "line", i)
		}
	}
	slog.Info("validate: extract OK", "extractions", len(lines))
	return nil
}

// validateLabel checks labeled.jsonl per 03-label.md.
func validateLabel(dataDir string) error {
	extractCount := countLines(filepath.Join(dataDir, "extracted.jsonl"))
	lines, err := readJSONL(filepath.Join(dataDir, "labeled.jsonl"))
	if err != nil {
		return fmt.Errorf("reading labeled.jsonl: %w", err)
	}
	if len(lines) != extractCount {
		return fmt.Errorf("labeled.jsonl has %d lines, extracted.jsonl has %d", len(lines), extractCount)
	}
	for i, raw := range lines {
		var l struct {
			Number int      `json:"number"`
			Labels []string `json:"labels"`
		}
		if err := json.Unmarshal(raw, &l); err != nil {
			continue
		}
		if l.Number != 0 && len(l.Labels) == 0 {
			slog.Warn("validate: issue has no labels", "number", l.Number, "line", i)
		}
	}
	slog.Info("validate: label OK", "labels", len(lines))
	return nil
}

// validateAggregate checks fix-themes.jsonl and fix-priority.md per 04-aggregate.md.
func validateAggregate(dataDir string) error {
	lines, err := readJSONL(filepath.Join(dataDir, "fix-themes.jsonl"))
	if err != nil {
		return fmt.Errorf("reading fix-themes.jsonl: %w", err)
	}
	if len(lines) == 0 {
		return fmt.Errorf("fix-themes.jsonl is empty")
	}
	for i, raw := range lines {
		var t struct {
			ThemeID      string `json:"theme_id"`
			Title        string `json:"title"`
			IssueNumbers []int  `json:"issue_numbers"`
			IssueCount   int    `json:"issue_count"`
		}
		if err := json.Unmarshal(raw, &t); err != nil {
			return fmt.Errorf("theme %d: invalid JSON: %w", i, err)
		}
		if t.ThemeID == "" || t.Title == "" {
			return fmt.Errorf("theme %d: missing theme_id or title", i)
		}
		if t.IssueCount != len(t.IssueNumbers) {
			return fmt.Errorf("theme %q: issue_count=%d but len(issue_numbers)=%d",
				t.ThemeID, t.IssueCount, len(t.IssueNumbers))
		}
	}
	info, err := os.Stat(filepath.Join(dataDir, "fix-priority.md"))
	if err != nil {
		return fmt.Errorf("fix-priority.md missing: %w", err)
	}
	if info.Size() < 2000 {
		return fmt.Errorf("fix-priority.md too small (%d bytes)", info.Size())
	}
	slog.Info("validate: aggregate OK", "themes", len(lines))
	return nil
}

// validateEvaluate checks evaluated.jsonl per 05-evaluate.md.
func validateEvaluate(dataDir string) error {
	issueCount := countLines(filepath.Join(dataDir, "issues.jsonl"))
	lines, err := readJSONL(filepath.Join(dataDir, "evaluated.jsonl"))
	if err != nil {
		return fmt.Errorf("reading evaluated.jsonl: %w", err)
	}
	if len(lines) != issueCount {
		return fmt.Errorf("evaluated.jsonl has %d lines, issues.jsonl has %d", len(lines), issueCount)
	}
	validVerdicts := map[string]bool{"yes": true, "partial": true, "no": true}
	for i, raw := range lines {
		var e struct {
			Number          int `json:"number"`
			ApplicableFixes []struct {
				ThemeID string `json:"theme_id"`
				Verdict string `json:"verdict"`
			} `json:"applicable_fixes"`
		}
		if err := json.Unmarshal(raw, &e); err != nil {
			continue
		}
		for _, f := range e.ApplicableFixes {
			if !validVerdicts[f.Verdict] {
				slog.Warn("validate: invalid verdict", "number", e.Number, "line", i, "verdict", f.Verdict)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(dataDir, "evaluation-summary.json")); err != nil {
		return fmt.Errorf("evaluation-summary.json missing: %w", err)
	}
	slog.Info("validate: evaluate OK", "evaluations", len(lines))
	return nil
}

// writeEvalSummary computes and writes evaluation-summary.json per 05-evaluate.md.
func writeEvalSummary(dataDir string) error {
	stats := computeEvalStats(filepath.Join(dataDir, "evaluated.jsonl"))

	// Build per-theme accuracy.
	themeAccuracy := make(map[string]map[string]int)
	lines, err := readJSONL(filepath.Join(dataDir, "evaluated.jsonl"))
	if err != nil {
		return err
	}
	for _, raw := range lines {
		var e struct {
			ApplicableFixes []struct {
				ThemeID string `json:"theme_id"`
				Verdict string `json:"verdict"`
			} `json:"applicable_fixes"`
		}
		if json.Unmarshal(raw, &e) != nil {
			continue
		}
		for _, f := range e.ApplicableFixes {
			if themeAccuracy[f.ThemeID] == nil {
				themeAccuracy[f.ThemeID] = make(map[string]int)
			}
			themeAccuracy[f.ThemeID][f.Verdict]++
		}
	}

	summary := struct {
		TotalEvaluated    int                       `json:"total_evaluated"`
		Verdicts          map[string]int             `json:"verdicts"`
		Unaddressed       int                       `json:"unaddressed"`
		MisattributionRate float64                   `json:"misattribution_rate"`
		ThemeAccuracy     map[string]map[string]int  `json:"theme_accuracy"`
	}{
		TotalEvaluated:    stats.total,
		Verdicts:          map[string]int{"yes": stats.yes, "partial": stats.partial, "no": stats.no},
		Unaddressed:       stats.unaddressed,
		MisattributionRate: stats.misattributionRate(),
		ThemeAccuracy:     themeAccuracy,
	}

	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dataDir, "evaluation-summary.json"), data, 0644)
}
