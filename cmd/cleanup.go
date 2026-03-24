package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/tijn/nodetester/internal/docker"
)

func init() {
	rootCmd.AddCommand(cleanupCmd)
}

var cleanupCmd = &cobra.Command{
	Use:   "cleanup",
	Short: "Remove all nodetester containers and networks",
	RunE:  runCleanup,
}

func runCleanup(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	dc, err := docker.New()
	if err != nil {
		return err
	}
	defer dc.Close()

	// Remove all nodetester-labeled containers.
	nodes, err := dc.ListNodes(ctx)
	if err != nil {
		return err
	}

	if len(nodes) == 0 {
		fmt.Println("No nodetester containers to clean up.")
	}

	for _, n := range nodes {
		fmt.Printf("[cleanup] removing container %s\n", n.Name)
		if err := dc.RemoveNode(ctx, n.Name); err != nil {
			fmt.Printf("[cleanup] WARNING: failed to remove %s: %v\n", n.Name, err)
		}
	}

	// Remove the network.
	fmt.Println("[cleanup] removing network nodetester-net")
	if err := dc.RemoveNetwork(ctx, "nodetester-net"); err != nil {
		fmt.Printf("[cleanup] WARNING: failed to remove network: %v\n", err)
	}

	fmt.Println("[cleanup] done")
	return nil
}
