package ui

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tijnverbeek2004/nodetester/internal/assert"
	"github.com/tijnverbeek2004/nodetester/internal/chaos"
	"github.com/tijnverbeek2004/nodetester/internal/devnet"
	"github.com/tijnverbeek2004/nodetester/internal/docker"
	"github.com/tijnverbeek2004/nodetester/internal/metrics"
	"github.com/tijnverbeek2004/nodetester/pkg/types"
)

var conditionPattern = regexp.MustCompile(`^(\S+)\.state\s*==\s*(\S+)$`)

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

	// Clean up any leftover containers and network from previous runs
	cleanup(ctx, dc)

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

	// Start stats collection in background
	statsCtx, statsCancel := context.WithCancel(ctx)
	defer statsCancel()
	statsCollector := metrics.NewStatsCollector(dc, nodeNames, 2*time.Second, func(stats map[string]types.ContainerStats) {
		ch <- statsUpdateMsg{stats: stats}
	})
	statsCollector.Start(statsCtx)

	// Execute chaos events and assertions
	collector := metrics.NewCollector(dc)
	executor := chaos.NewExecutor(dc, networkName)
	checker := assert.NewChecker(dc, networkName)

	hasWork := len(sc.Events) > 0 || len(sc.Assertions) > 0
	if hasWork {
		ch <- phaseStartMsg{name: "Execute timeline", info: "building..."}

		if err := executeTimeline(ctx, sc, executor, checker, collector, dc, ch); err != nil {
			ch <- phaseErrorMsg{name: "Execute timeline", err: err}
			ch <- scenarioDoneMsg{err: err}
			return
		}
	}

	// Stop stats collection
	statsCancel()

	// Write report
	absReport, _ := filepath.Abs(reportPath)
	if err := collector.WriteReportWithStats(ctx, absReport, statsCollector.History()); err != nil {
		ch <- scenarioDoneMsg{err: err}
		return
	}

	// Gather final state
	nodes, _ := collector.Snapshot(ctx)
	events := collector.Events()
	assertions := collector.Assertions()

	// Clean up containers and network
	cleanup(ctx, dc)

	ch <- scenarioDoneMsg{
		nodes:      nodes,
		events:     events,
		assertions: assertions,
		stats:      statsCollector.History(),
		reportPath: absReport,
	}
}

// cleanup removes all nodetester containers and the network.
func cleanup(ctx context.Context, dc *docker.Client) {
	nodes, err := dc.ListNodes(ctx)
	if err != nil {
		return
	}
	for _, n := range nodes {
		_ = dc.RemoveNode(ctx, n.Name)
	}
	_ = dc.RemoveNetwork(ctx, "nodetester-net")
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

// timelineItem represents either a chaos event or an assertion in the unified timeline.
type timelineItem struct {
	at        time.Duration
	isAssert  bool
	eventIdx  int    // index into scenario.Events (when isAssert=false)
	assertIdx int    // index into scenario.Assertions (when isAssert=true)
	condition string // optional condition to evaluate before execution
}

// expandEvents expands repeat/loop events into individual timeline items.
func expandEvents(events []types.Event) []timelineItem {
	var items []timelineItem
	for i, e := range events {
		if e.Every.Duration > 0 {
			// Repeated event: generate instances
			at := e.At.Duration
			count := 0
			for {
				if e.Count > 0 && count >= e.Count {
					break
				}
				if e.Until.Duration > 0 && at > e.Until.Duration {
					break
				}
				items = append(items, timelineItem{
					at:        at,
					isAssert:  false,
					eventIdx:  i,
					condition: e.If,
				})
				count++
				at += e.Every.Duration
			}
		} else {
			items = append(items, timelineItem{
				at:        e.At.Duration,
				isAssert:  false,
				eventIdx:  i,
				condition: e.If,
			})
		}
	}
	return items
}

func executeTimeline(ctx context.Context, sc *types.Scenario, exec *chaos.Executor, checker *assert.Checker, col *metrics.Collector, dc *docker.Client, ch chan<- tea.Msg) error {
	// Expand repeated events and build unified timeline
	items := expandEvents(sc.Events)
	for i, a := range sc.Assertions {
		items = append(items, timelineItem{at: a.At.Duration, isAssert: true, assertIdx: i})
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].at < items[j].at
	})

	totalItems := len(items)

	// Send full timeline to TUI
	entries := make([]eventEntry, totalItems)
	for i, item := range items {
		if item.isAssert {
			a := sc.Assertions[item.assertIdx]
			entries[i] = eventEntry{
				at:     formatDuration(a.At.Duration),
				action: "assert:" + a.Type,
				target: a.Target,
			}
		} else {
			e := sc.Events[item.eventIdx]
			action := e.Action
			if item.condition != "" {
				action += " [if]"
			}
			entries[i] = eventEntry{
				at:     formatDuration(item.at),
				action: action,
				target: e.Target,
			}
		}
	}
	ch <- eventScheduledMsg{events: entries}
	ch <- phaseUpdateMsg{name: "Execute timeline", info: fmt.Sprintf("0/%d", totalItems)}

	start := time.Now()

	// Group items by time for parallel execution
	i := 0
	for i < len(items) {
		// Collect all items at the same time
		groupStart := i
		groupAt := items[i].at
		for i < len(items) && items[i].at == groupAt {
			i++
		}
		group := items[groupStart:i]

		// Wait until the scheduled time
		waitDur := groupAt - time.Since(start)
		if waitDur > 0 {
			select {
			case <-time.After(waitDur):
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		if len(group) == 1 {
			// Single item — run directly
			displayIdx := groupStart
			executeOneItem(ctx, sc, group[0], displayIdx, totalItems, exec, checker, col, dc, ch)
		} else {
			// Multiple items at same time — run in parallel
			var wg sync.WaitGroup
			for gi, item := range group {
				displayIdx := groupStart + gi
				ch <- eventRunningMsg{index: displayIdx}
				wg.Add(1)
				go func(item timelineItem, displayIdx int) {
					defer wg.Done()
					runItem(ctx, sc, item, displayIdx, exec, checker, col, dc, ch)
				}(item, displayIdx)
			}
			wg.Wait()
			ch <- phaseUpdateMsg{name: "Execute timeline", info: fmt.Sprintf("%d/%d", groupStart+len(group), totalItems)}
		}
	}

	ch <- phaseDoneMsg{name: "Execute timeline", info: fmt.Sprintf("%d/%d complete", totalItems, totalItems)}
	return nil
}

// executeOneItem handles a single timeline item (with running/done messages and phase update).
func executeOneItem(ctx context.Context, sc *types.Scenario, item timelineItem, displayIdx, totalItems int, exec *chaos.Executor, checker *assert.Checker, col *metrics.Collector, dc *docker.Client, ch chan<- tea.Msg) {
	ch <- eventRunningMsg{index: displayIdx}
	ch <- phaseUpdateMsg{name: "Execute timeline", info: fmt.Sprintf("%d/%d", displayIdx+1, totalItems)}
	runItem(ctx, sc, item, displayIdx, exec, checker, col, dc, ch)
}

// runItem executes a single timeline item (event or assertion) and sends the done message.
func runItem(ctx context.Context, sc *types.Scenario, item timelineItem, displayIdx int, exec *chaos.Executor, checker *assert.Checker, col *metrics.Collector, dc *docker.Client, ch chan<- tea.Msg) {
	if item.isAssert {
		a := sc.Assertions[item.assertIdx]
		result := checker.Check(ctx, a)
		col.RecordAssertion(result)
		if result.Success {
			ch <- eventDoneMsg{index: displayIdx, success: true}
		} else {
			ch <- eventDoneMsg{index: displayIdx, success: false, errMsg: result.Message}
		}
		return
	}

	// Check condition before running event
	event := sc.Events[item.eventIdx]
	if item.condition != "" {
		met, err := evaluateCondition(ctx, item.condition, dc)
		if err != nil {
			ch <- eventDoneMsg{index: displayIdx, success: false, errMsg: fmt.Sprintf("condition error: %s", err)}
			return
		}
		if !met {
			col.RecordEvent(event.Action, event.Target, nil)
			ch <- eventDoneMsg{index: displayIdx, skipped: true}
			return
		}
	}

	err := exec.Run(ctx, event.Action, event.Target, event.Params)
	col.RecordEvent(event.Action, event.Target, err)
	if err != nil {
		ch <- eventDoneMsg{index: displayIdx, success: false, errMsg: err.Error()}
	} else {
		ch <- eventDoneMsg{index: displayIdx, success: true}
	}
}

// evaluateCondition checks a simple condition string like "node-1.state == exited".
func evaluateCondition(ctx context.Context, condition string, dc *docker.Client) (bool, error) {
	condition = strings.TrimSpace(condition)
	match := conditionPattern.FindStringSubmatch(condition)
	if match == nil {
		return false, fmt.Errorf("unsupported condition syntax: %q (use: node-X.state == running|exited)", condition)
	}

	nodeName := match[1]
	expectedState := match[2]

	nodes, err := dc.ListNodes(ctx)
	if err != nil {
		return false, fmt.Errorf("listing nodes: %w", err)
	}
	for _, n := range nodes {
		if n.Name == nodeName {
			return n.State == expectedState, nil
		}
	}
	return false, fmt.Errorf("node %q not found", nodeName)
}

func formatDuration(d time.Duration) string {
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%d:%02d", m, s)
}
