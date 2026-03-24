package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/tijn/nodetester/internal/chaos"
	"github.com/tijn/nodetester/internal/devnet"
	"github.com/tijn/nodetester/internal/docker"
	"github.com/tijn/nodetester/internal/metrics"
	"github.com/tijn/nodetester/internal/scenario"
	"github.com/tijn/nodetester/pkg/types"
)

var reportPath string

func init() {
	runCmd.Flags().StringVarP(&reportPath, "report", "r", "report.json", "path for the JSON report output")
	rootCmd.AddCommand(runCmd)
}

var runCmd = &cobra.Command{
	Use:   "run [scenario.yaml]",
	Short: "Run a chaos test scenario",
	Args:  cobra.ExactArgs(1),
	RunE:  runScenario,
}

func runScenario(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// 1. Parse scenario
	scenarioPath := args[0]
	sc, err := scenario.Load(scenarioPath)
	if err != nil {
		return err
	}
	fmt.Printf("[run] loaded scenario from %s: %d nodes, %d events\n",
		scenarioPath, sc.Nodes.Count, len(sc.Events))

	// 2. Connect to Docker
	dc, err := docker.New()
	if err != nil {
		return err
	}
	defer dc.Close()

	// 3. Resolve image — Ethereum preset defaults to the geth image.
	imageName := sc.Nodes.Image
	if sc.Nodes.Preset == "ethereum" && imageName == "" {
		imageName = devnet.DefaultEthImage()
		sc.Nodes.Image = imageName
	}

	fmt.Printf("[run] pulling image %s...\n", imageName)
	if err := dc.PullImage(ctx, imageName); err != nil {
		return err
	}

	// 4. Create network
	networkName := "nodetester-net"
	if sc.Nodes.Network != "" {
		networkName = sc.Nodes.Network
	}
	fmt.Printf("[run] creating network %s\n", networkName)
	if _, err := dc.CreateNetwork(ctx, networkName); err != nil {
		return fmt.Errorf("creating network: %w", err)
	}

	// 5. Create containers
	nodeNames := make([]string, sc.Nodes.Count)
	for i := 1; i <= sc.Nodes.Count; i++ {
		name := fmt.Sprintf("node-%d", i)
		nodeNames[i-1] = name
		fmt.Printf("[run] creating %s\n", name)
		if _, err := dc.CreateNode(ctx, name, networkName, sc.Nodes); err != nil {
			return fmt.Errorf("creating %s: %w", name, err)
		}
	}
	fmt.Printf("[run] all %d containers running\n", sc.Nodes.Count)

	// 6. Preset-specific setup
	if sc.Nodes.Preset == "ethereum" {
		eth := devnet.NewEthDevnet(dc, networkName, *sc.Nodes.Ethereum)
		if err := eth.Setup(ctx, nodeNames); err != nil {
			return fmt.Errorf("ethereum devnet setup: %w", err)
		}
		if err := eth.WaitForBlocks(ctx); err != nil {
			return fmt.Errorf("ethereum devnet: %w", err)
		}
	}

	// 7. Custom binary injection
	if sc.Nodes.Binary != nil {
		if err := injectBinary(ctx, dc, nodeNames, sc.Nodes.Binary); err != nil {
			return fmt.Errorf("injecting binary: %w", err)
		}
	}

	// 8. Execute chaos events
	collector := metrics.NewCollector(dc)
	executor := chaos.NewExecutor(dc, networkName)

	if len(sc.Events) > 0 {
		if err := executeEvents(ctx, sc, executor, collector); err != nil {
			return err
		}
	}

	// 9. Write report
	absReport, _ := filepath.Abs(reportPath)
	if err := collector.WriteReport(ctx, absReport); err != nil {
		return err
	}

	fmt.Println("[run] scenario complete")
	return nil
}

// injectBinary copies a host binary into every container and starts it.
func injectBinary(ctx context.Context, dc *docker.Client, nodeNames []string, bin *types.CustomBinary) error {
	// Resolve the binary path relative to CWD.
	absPath, err := filepath.Abs(bin.Path)
	if err != nil {
		return fmt.Errorf("resolving binary path: %w", err)
	}
	if _, err := os.Stat(absPath); err != nil {
		return fmt.Errorf("binary not found at %s: %w", absPath, err)
	}

	binaryName := filepath.Base(absPath)
	fmt.Printf("[binary] injecting %s into %d containers\n", binaryName, len(nodeNames))

	for _, name := range nodeNames {
		// Copy the binary into /usr/local/bin/ inside the container.
		fmt.Printf("[binary] copying %s to %s\n", binaryName, name)
		if err := dc.CopyFileToContainer(ctx, name, "/usr/local/bin/", binaryName, absPath); err != nil {
			return fmt.Errorf("copying binary to %s: %w", name, err)
		}

		// Build the command: /usr/local/bin/<binary> <args...>
		execCmd := fmt.Sprintf("/usr/local/bin/%s", binaryName)
		for _, arg := range bin.Args {
			execCmd += " " + arg
		}
		execCmd += " > /var/log/p2p-node.log 2>&1 &"

		fmt.Printf("[binary] starting %s on %s\n", binaryName, name)
		if _, err := dc.Exec(ctx, name, []string{"sh", "-c", execCmd}); err != nil {
			return fmt.Errorf("starting binary on %s: %w", name, err)
		}
	}

	fmt.Printf("[binary] %s running on all nodes\n", binaryName)
	return nil
}

// executeEvents schedules and runs chaos events in order of their "at" time.
func executeEvents(ctx context.Context, sc *types.Scenario, exec *chaos.Executor, col *metrics.Collector) error {
	// Sort events by scheduled time.
	type indexedEvent struct {
		index int
		at    time.Duration
	}
	sorted := make([]indexedEvent, len(sc.Events))
	for i, e := range sc.Events {
		sorted[i] = indexedEvent{index: i, at: e.At.Duration}
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].at < sorted[j].at
	})

	start := time.Now()

	for _, ie := range sorted {
		event := sc.Events[ie.index]
		// Wait until the scheduled time.
		waitDur := event.At.Duration - time.Since(start)
		if waitDur > 0 {
			fmt.Printf("[run] waiting %s for next event...\n", waitDur.Round(time.Second))
			select {
			case <-time.After(waitDur):
			case <-ctx.Done():
				fmt.Println("[run] interrupted, stopping early")
				return ctx.Err()
			}
		}

		fmt.Printf("[run] executing: %s on %s\n", event.Action, event.Target)
		err := exec.Run(ctx, event.Action, event.Target, event.Params)
		col.RecordEvent(event.Action, event.Target, err)
		if err != nil {
			fmt.Printf("[run] WARNING: event failed: %v\n", err)
		}
	}

	return nil
}
