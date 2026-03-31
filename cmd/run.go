package cmd

import (
	"github.com/spf13/cobra"

	"github.com/tijnverbeek2004/nodetester/internal/ci"
	"github.com/tijnverbeek2004/nodetester/internal/scenario"
	"github.com/tijnverbeek2004/nodetester/internal/ui"
)

var (
	reportPath string
	ciMode     bool
)

func init() {
	runCmd.Flags().StringVarP(&reportPath, "report", "r", "report.json", "path for the JSON report output")
	runCmd.Flags().BoolVar(&ciMode, "ci", false, "CI mode: no TUI, structured stdout, exit code based on assertions")
	rootCmd.AddCommand(runCmd)
}

var runCmd = &cobra.Command{
	Use:   "run [scenario.yaml]",
	Short: "Run a chaos test scenario",
	Args:  cobra.ExactArgs(1),
	RunE:  runScenario,
}

func runScenario(cmd *cobra.Command, args []string) error {
	scenarioPath := args[0]
	sc, err := scenario.Load(scenarioPath)
	if err != nil {
		return err
	}

	if ciMode {
		return ci.Run(scenarioPath, sc, reportPath)
	}
	return ui.Run(scenarioPath, sc, reportPath)
}
