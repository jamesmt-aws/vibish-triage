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

var maxEMRounds int

func init() {
	planCmd.Flags().StringVar(&configFile, "config", "triage.yaml", "path to config file")
	planCmd.Flags().DurationVar(&timeout, "timeout", 90*time.Minute, "max time per LLM step")
	planCmd.Flags().StringVar(&dataDir, "data-dir", "./data", "directory for input/output data")
	planCmd.Flags().StringVar(&promptsDir, "prompts-dir", "./prompts", "directory containing prompt templates")
	planCmd.Flags().IntVar(&maxEMRounds, "max-em-rounds", 2, "maximum EM iterations for action plan")

	rootCmd.AddCommand(planCmd)
}

var planCmd = &cobra.Command{
	Use:   "plan",
	Short: "Classify issues and produce an action plan",
	Long: `Plan classifies every open issue and produces a prioritized work plan.

Three phases:
  1. Parallel Sonnet calls classify each issue (kind, action, priority, effort).
     Results are cached by input hash; re-runs skip unchanged issues.
  2. Code seeds draft actions from themes + classifications.
  3. EM-style iteration: Sonnet assigns issues to actions, Opus refines actions.
     Default 2 rounds; use --max-em-rounds to adjust.

Requires prior run output: extracted.jsonl, evaluated.jsonl, fix-themes.jsonl.

Output: plan-events.jsonl, action-plan.jsonl, plan-summary.json

Examples:

  vibish-triage plan --config examples/karpenter.yaml
  vibish-triage plan --config examples/karpenter.yaml --max-em-rounds 3`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(configFile)
		if err != nil {
			return err
		}

		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		if err := pipeline.Plan(ctx, cfg, dataDir, promptsDir, timeout, maxEMRounds); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}

		return nil
	},
}
