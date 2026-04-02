package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jamesmt/vibish-triage/internal/config"
	"github.com/jamesmt/vibish-triage/internal/pipeline"
	"github.com/spf13/cobra"
)

var (
	configFile string
	step       string
	timeout    time.Duration
	dataDir    string
	promptsDir string
	workers    int
)

func init() {
	runCmd.Flags().StringVar(&configFile, "config", "triage.yaml", "path to config file")
	runCmd.Flags().StringVar(&step, "step", "all", "step to run: download, extract, label, aggregate, evaluate, or all")
	runCmd.Flags().DurationVar(&timeout, "timeout", 90*time.Minute, "max time per LLM step")
	runCmd.Flags().StringVar(&dataDir, "data-dir", "./data", "directory for input/output data")
	runCmd.Flags().StringVar(&promptsDir, "prompts-dir", "./prompts", "directory containing prompt templates")
	runCmd.Flags().IntVar(&workers, "workers", 16, "parallel workers for downloading comments")

	rootCmd.AddCommand(runCmd)
}

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Run the triage pipeline",
	Long: `Run executes the issue triage pipeline: download, extract, aggregate, evaluate.

Use --step to run a single step. Steps:

  download   Fetch issues from GitHub via gh CLI
  extract    Diagnose each issue and propose fixes (Sonnet, parallel)
  label      Normalize proposed fixes into canonical labels (Sonnet, parallel)
  aggregate  Cluster labels into themes, rank by impact (Opus, single call)
  evaluate   Verify theme assignments per issue (Sonnet, parallel)
  all        Run all steps in sequence (default)

Examples:

  vibish-triage run --config examples/karpenter.yaml
  vibish-triage run --config triage.yaml --step download
  vibish-triage run --config triage.yaml --step extract --timeout 30m`,
	RunE: func(cmd *cobra.Command, args []string) error {
		validSteps := map[string]bool{"all": true, "download": true, "extract": true, "label": true, "aggregate": true, "evaluate": true}
		if !validSteps[step] {
			return fmt.Errorf("invalid step %q, must be one of: all, download, extract, aggregate, evaluate", step)
		}

		cfg, err := config.Load(configFile)
		if err != nil {
			return err
		}

		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		if err := pipeline.Run(ctx, cfg, step, dataDir, promptsDir, timeout, workers); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}

		return nil
	},
}
