package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "vibish-triage",
	Short: "Triage open issues on any OSS repo using LLM-assisted analysis",
	Long: `vibish-triage downloads open issues from GitHub, diagnoses each one,
clusters proposed fixes into themes, and verifies the clustering.
Produces a ranked list of high-leverage fixes.

Uses kiro-cli as the LLM backend.`,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
