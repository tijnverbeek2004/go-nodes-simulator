package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "nodetester",
	Short: "Spin up, manage, and chaos-test distributed node systems",
	Long: `nodetester is a CLI tool for chaos-testing distributed systems.
It creates real Docker containers, runs failure scenarios, and reports results.`,
}

// Execute is called from main.go — the single entrypoint.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
