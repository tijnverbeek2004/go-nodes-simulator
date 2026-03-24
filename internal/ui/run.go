package ui

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tijnverbeek2004/nodetester/internal/chaos"
	"github.com/tijnverbeek2004/nodetester/internal/devnet"
	"github.com/tijnverbeek2004/nodetester/internal/docker"
	"github.com/tijnverbeek2004/nodetester/internal/metrics"
	"github.com/tijnverbeek2004/nodetester/pkg/types"
)

// Run starts the TUI and executes the scenario with live visual feedback.
func Run(scenarioPath string, scenario *types.Scenario, reportPath string) error {
	ch := make(chan tea.Msg, 100)

	m := newModel(scenarioPath, scenario, ch)

	go executeScenario(scenario, reportPath, ch)

	p := tea.NewProgram(m)
	result, err := p.Run()
	if err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}

	final := result.(model)
	if final.err != nil {
		return final.err
	}
	return nil
}

func executeScenario(sc *types.Scenario, reportPath string, ch chan<- tea.Msg) {
	defer close(ch)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// Connect to Docker
	dc, err := docker.New()
	if err != nil {
		ch <- scenarioDoneMsg{err: err}
		return
	}
	defer dc.Close()

	// Resolve image
	imageName := sc.Nodes.Image
	if sc.Nodes.Preset == "ethereum" && imageName == "" {
		imageName = devnet.DefaultEthImage()
		sc.Nodes.Image = imageName
	}

	// Pull image
	ch <- phaseStartMsg{name: "Pull image", info: imageName}
	if err := dc.PullImage(ctx, imageName, io.Discard); err != nil {
		ch <- phaseErrorMsg{name: "Pull image", err: err}
		ch <- scenarioDoneMsg{err: err}
		return
	}
	ch <- phaseDoneMsg{name: "Pull image", info: imageName}

	// Create network
	networkName := "nodetester-net"
	if sc.Nodes.Network != "" {
		networkName = sc.Nodes.Network
	}
	ch <- phaseStartMsg{name: "Create network", info: networkName}
	if _, err := dc.CreateNetwork(ctx, networkName); err != nil {
		ch <- phaseErrorMsg{name: "Create network", err: err}
		ch <- scenarioDoneMsg{err: fmt.Errorf("creating network: %w", err)}
		return
	}
	ch <- phaseDoneMsg{name: "Create network", info: networkName}

	// Create containers
	ch <- phaseStartMsg{name: "Spin up nodes", info: fmt.Sprintf("0/%d", sc.Nodes.Count)}
	nodeNames := make([]string, sc.Nodes.Count)
	for i := 1; i <= sc.Nodes.Count; i++ {
		name := fmt.Sprintf("node-%d", i)
		nodeNames[i-1] = name
		ch <- phaseUpdateMsg{name: "Spin up nodes", info: fmt.Sprintf("%d/%d  %s", i, sc.Nodes.Count, name)}
		if _, err := dc.CreateNode(ctx, name, networkName, sc.Nodes); err != nil {
			ch <- phaseErrorMsg{name: "Spin up nodes", err: err}
			ch <- scenarioDoneMsg{err: fmt.Errorf("creating %s: %w", name, err)}
			return
		}
	}
	ch <- phaseDoneMsg{name: "Spin up nodes", info: fmt.Sprintf("%d containers", sc.Nodes.Count)}

	// Preset setup
	if sc.Nodes.Preset == "ethereum" {
		ch <- phaseStartMsg{name: "Ethereum devnet", info: "initializing..."}
		eth := devnet.NewEthDevnet(dc, networkName, *sc.Nodes.Ethereum)

		statusFn := func(msg string) {
			ch <- phaseUpdateMsg{name: "Ethereum devnet", info: msg}
		}

		if err := eth.Setup(ctx, nodeNames, statusFn); err != nil {
			ch <- phaseErrorMsg{name: "Ethereum devnet", err: err}
			ch <- scenarioDoneMsg{err: fmt.Errorf("ethereum devnet setup: %w", err)}
			return
		}

		ch <- phaseUpdateMsg{name: "Ethereum devnet", info: "waiting for blocks..."}
		if err := eth.WaitForBlocks(ctx); err != nil {
			ch <- phaseErrorMsg{name: "Ethereum devnet", err: err}
			ch <- scenarioDoneMsg{err: fmt.Errorf("ethereum devnet: %w", err)}
			return
		}
		ch <- phaseDoneMsg{name: "Ethereum devnet", info: fmt.Sprintf("%d sealers ready", sc.Nodes.Count)}
	}

	// Custom binary injection
	if sc.Nodes.Binary != nil {
		ch <- phaseStartMsg{name: "Inject binary", info: sc.Nodes.Binary.Path}
		if err := injectBinary(ctx, dc, nodeNames, sc.Nodes.Binary); err != nil {
			ch <- phaseErrorMsg{name: "Inject binary", err: err}
			ch <- scenarioDoneMsg{err: fmt.Errorf("injecting binary: %w", err)}
			return
		}
		ch <- phaseDoneMsg{name: "Inject binary"}
	}

	// Execute chaos events
	collector := metrics.NewCollector(dc)
	executor := chaos.NewExecutor(dc, networkName)

	if len(sc.Events) > 0 {
		ch <- phaseStartMsg{name: "Execute events", info: fmt.Sprintf("0/%d", len(sc.Events))}

		if err := executeEvents(ctx, sc, executor, collector, ch); err != nil {
			ch <- phaseErrorMsg{name: "Execute events", err: err}
			ch <- scenarioDoneMsg{err: err}
			return
		}
		ch <- phaseDoneMsg{name: "Execute events", info: fmt.Sprintf("%d/%d complete", len(sc.Events), len(sc.Events))}
	}

	// Write report
	absReport, _ := filepath.Abs(reportPath)
	if err := collector.WriteReport(ctx, absReport); err != nil {
		ch <- scenarioDoneMsg{err: err}
		return
	}

	// Gather final state
	nodes, _ := collector.Snapshot(ctx)
	events := collector.Events()

	ch <- scenarioDoneMsg{
		nodes:      nodes,
		events:     events,
		reportPath: absReport,
	}
}

func injectBinary(ctx context.Context, dc *docker.Client, nodeNames []string, bin *types.CustomBinary) error {
	absPath, err := filepath.Abs(bin.Path)
	if err != nil {
		return fmt.Errorf("resolving binary path: %w", err)
	}
	if _, err := os.Stat(absPath); err != nil {
		return fmt.Errorf("binary not found at %s: %w", absPath, err)
	}

	binaryName := filepath.Base(absPath)
	for _, name := range nodeNames {
		if err := dc.CopyFileToContainer(ctx, name, "/usr/local/bin/", binaryName, absPath); err != nil {
			return fmt.Errorf("copying binary to %s: %w", name, err)
		}
		execCmd := fmt.Sprintf("/usr/local/bin/%s", binaryName)
		for _, arg := range bin.Args {
			execCmd += " " + arg
		}
		execCmd += " > /var/log/p2p-node.log 2>&1 &"
		if _, err := dc.Exec(ctx, name, []string{"sh", "-c", execCmd}); err != nil {
			return fmt.Errorf("starting binary on %s: %w", name, err)
		}
	}
	return nil
}

func executeEvents(ctx context.Context, sc *types.Scenario, exec *chaos.Executor, col *metrics.Collector, ch chan<- tea.Msg) error {
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

	// Send the full event list to the TUI for display
	entries := make([]eventEntry, len(sorted))
	for i, ie := range sorted {
		event := sc.Events[ie.index]
		entries[i] = eventEntry{
			at:     formatDuration(event.At.Duration),
			action: event.Action,
			target: event.Target,
		}
	}
	ch <- eventScheduledMsg{events: entries}

	start := time.Now()

	for displayIdx, ie := range sorted {
		event := sc.Events[ie.index]

		waitDur := event.At.Duration - time.Since(start)
		if waitDur > 0 {
			select {
			case <-time.After(waitDur):
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		ch <- eventRunningMsg{index: displayIdx}
		ch <- phaseUpdateMsg{name: "Execute events", info: fmt.Sprintf("%d/%d", displayIdx+1, len(sc.Events))}

		err := exec.Run(ctx, event.Action, event.Target, event.Params)
		col.RecordEvent(event.Action, event.Target, err)

		if err != nil {
			ch <- eventDoneMsg{index: displayIdx, success: false, errMsg: err.Error()}
		} else {
			ch <- eventDoneMsg{index: displayIdx, success: true}
		}
	}

	return nil
}

func formatDuration(d time.Duration) string {
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%d:%02d", m, s)
}
