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

func init() {
	reportCmd.Flags().StringVar(&configFile, "config", "triage.yaml", "path to config file")
	reportCmd.Flags().DurationVar(&timeout, "timeout", 90*time.Minute, "max time per LLM step")
	reportCmd.Flags().StringVar(&dataDir, "data-dir", "./data", "directory for input/output data")
	reportCmd.Flags().IntVar(&workers, "workers", 16, "parallel workers for downloading comments")

	rootCmd.AddCommand(reportCmd)
}

var reportCmd = &cobra.Command{
	Use:   "report",
	Short: "Report on new issues since last full run",
	Long: `Downloads current issues, finds ones not in the last run, diagnoses them,
and maps them to existing themes. Requires a prior 'run' (themes must exist).

Output: data/report-YYYYMMDD.md`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(configFile)
		if err != nil {
			return err
		}

		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		if err := pipeline.Report(ctx, cfg, dataDir, timeout, workers); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}

		return nil
	},
}
