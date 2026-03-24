package cmd

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/tijn/nodetester/internal/docker"
	"github.com/tijn/nodetester/internal/metrics"
)

func init() {
	rootCmd.AddCommand(statusCmd)
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the status of all nodetester nodes",
	RunE:  showStatus,
}

func showStatus(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	dc, err := docker.New()
	if err != nil {
		return err
	}
	defer dc.Close()

	collector := metrics.NewCollector(dc)
	nodes, err := collector.Snapshot(ctx)
	if err != nil {
		return err
	}

	if len(nodes) == 0 {
		fmt.Println("No nodetester nodes found. Run a scenario first.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tCONTAINER ID\tSTATE\tRESTARTS")
	for _, n := range nodes {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\n", n.Name, n.ContainerID, n.State, n.RestartCount)
	}
	return w.Flush()
}
