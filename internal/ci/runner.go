package ci

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

	"github.com/tijnverbeek2004/nodetester/internal/assert"
	"github.com/tijnverbeek2004/nodetester/internal/chaos"
	"github.com/tijnverbeek2004/nodetester/internal/devnet"
	"github.com/tijnverbeek2004/nodetester/internal/docker"
	"github.com/tijnverbeek2004/nodetester/internal/metrics"
	"github.com/tijnverbeek2004/nodetester/pkg/types"
)

var conditionPattern = regexp.MustCompile(`^(\S+)\.state\s*==\s*(\S+)$`)

// ErrAssertionsFailed is returned when one or more assertions fail.
var ErrAssertionsFailed = fmt.Errorf("assertions failed")

// Run executes the scenario in CI mode: no TUI, structured stdout, exit code based on assertions.
func Run(scenarioPath string, sc *types.Scenario, reportPath string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	dc, err := docker.New()
	if err != nil {
		return err
	}
	defer dc.Close()

	// Clean up leftovers
	cleanup(ctx, dc)

	// Resolve image
	imageName := sc.Nodes.Image
	if imageName == "" {
		imageName = devnet.DefaultImageForPreset(sc.Nodes.Preset)
		sc.Nodes.Image = imageName
	}

	// Pull image
	logPhase("pull", "Pulling image %s", imageName)
	if err := dc.PullImage(ctx, imageName, io.Discard); err != nil {
		return err
	}
	logDone("pull", "Image ready")

	// Create network
	networkName := "nodetester-net"
	if sc.Nodes.Network != "" {
		networkName = sc.Nodes.Network
	}
	logPhase("network", "Creating network %s", networkName)
	if _, err := dc.CreateNetwork(ctx, networkName); err != nil {
		return fmt.Errorf("creating network: %w", err)
	}
	logDone("network", "Network ready")

	// Create containers
	logPhase("nodes", "Spinning up %d nodes", sc.Nodes.Count)
	nodeNames := make([]string, sc.Nodes.Count)
	for i := 1; i <= sc.Nodes.Count; i++ {
		name := fmt.Sprintf("node-%d", i)
		nodeNames[i-1] = name
		if _, err := dc.CreateNode(ctx, name, networkName, sc.Nodes); err != nil {
			return fmt.Errorf("creating %s: %w", name, err)
		}
	}
	logDone("nodes", "%d containers running", sc.Nodes.Count)

	// Preset setup
	if sc.Nodes.Preset != "" {
		logPhase(sc.Nodes.Preset, "Setting up %s devnet", sc.Nodes.Preset)
		statusFn := func(msg string) {
			logInfo(sc.Nodes.Preset, "%s", msg)
		}
		if err := devnet.SetupPreset(ctx, sc, dc, networkName, nodeNames, statusFn); err != nil {
			return fmt.Errorf("%s devnet setup: %w", sc.Nodes.Preset, err)
		}
		if err := devnet.WaitForBlocks(ctx, sc.Nodes.Preset, dc, networkName, nodeNames, sc); err != nil {
			return fmt.Errorf("%s devnet: %w", sc.Nodes.Preset, err)
		}
		logDone(sc.Nodes.Preset, "%d nodes ready", sc.Nodes.Count)
	}

	// Custom binary injection
	if sc.Nodes.Binary != nil {
		logPhase("binary", "Injecting %s", sc.Nodes.Binary.Path)
		if err := injectBinary(ctx, dc, nodeNames, sc.Nodes.Binary); err != nil {
			return fmt.Errorf("injecting binary: %w", err)
		}
		logDone("binary", "Binary injected")
	}

	// Stats collection
	statsCtx, statsCancel := context.WithCancel(ctx)
	defer statsCancel()
	statsCollector := metrics.NewStatsCollector(dc, nodeNames, 2*time.Second, nil)
	statsCollector.Start(statsCtx)

	// Execute timeline
	collector := metrics.NewCollector(dc)
	executor := chaos.NewExecutor(dc, networkName)
	checker := assert.NewChecker(dc, networkName)

	if len(sc.Events) > 0 || len(sc.Assertions) > 0 {
		logPhase("timeline", "Executing timeline")
		if err := executeTimeline(ctx, sc, executor, checker, collector, dc); err != nil {
			return err
		}
		logDone("timeline", "Timeline complete")
	}

	statsCancel()

	// Write report
	absReport, _ := filepath.Abs(reportPath)
	if err := collector.WriteReportWithStats(ctx, absReport, statsCollector.History()); err != nil {
		return err
	}

	// Print results
	nodes, _ := collector.Snapshot(ctx)
	events := collector.Events()
	assertions := collector.Assertions()

	// Cleanup before printing results
	cleanup(ctx, dc)

	printResults(nodes, events, assertions, absReport)

	// Exit with error if any assertions failed
	for _, a := range assertions {
		if !a.Success {
			return ErrAssertionsFailed
		}
	}

	return nil
}

func logPhase(tag, format string, args ...interface{}) {
	fmt.Printf("[%s] %s\n", tag, fmt.Sprintf(format, args...))
}

func logDone(tag, format string, args ...interface{}) {
	fmt.Printf("[%s] OK: %s\n", tag, fmt.Sprintf(format, args...))
}

func logInfo(tag, format string, args ...interface{}) {
	fmt.Printf("[%s]   %s\n", tag, fmt.Sprintf(format, args...))
}

func printResults(nodes []types.NodeStatus, events []types.EventRecord, assertions []types.AssertionResult, reportPath string) {
	fmt.Println()
	fmt.Println("=== RESULTS ===")
	fmt.Println()

	// Nodes
	fmt.Printf("%-14s %-14s %-12s %s\n", "NODE", "CONTAINER", "STATE", "RESTARTS")
	fmt.Println(strings.Repeat("-", 50))
	for _, n := range nodes {
		fmt.Printf("%-14s %-14s %-12s %d\n", n.Name, n.ContainerID, n.State, n.RestartCount)
	}

	// Events
	if len(events) > 0 {
		fmt.Println()
		fmt.Printf("%-14s %-20s %-8s %s\n", "ACTION", "TARGET", "RESULT", "ERROR")
		fmt.Println(strings.Repeat("-", 55))
		for _, e := range events {
			result := "PASS"
			if !e.Success {
				result = "FAIL"
			}
			errMsg := ""
			if e.Error != "" {
				errMsg = e.Error
			}
			fmt.Printf("%-14s %-20s %-8s %s\n", e.Action, e.Target, result, errMsg)
		}
	}

	// Assertions
	if len(assertions) > 0 {
		fmt.Println()
		passed, total := 0, len(assertions)
		fmt.Printf("%-14s %-20s %-8s %s\n", "TYPE", "TARGET", "RESULT", "MESSAGE")
		fmt.Println(strings.Repeat("-", 55))
		for _, a := range assertions {
			result := "PASS"
			if a.Success {
				passed++
			} else {
				result = "FAIL"
			}
			fmt.Printf("%-14s %-20s %-8s %s\n", a.Type, a.Target, result, a.Message)
		}
		fmt.Printf("\nAssertions: %d/%d passed\n", passed, total)
	}

	fmt.Printf("\nReport: %s\n", reportPath)
}

// Timeline execution (mirrors ui/run.go logic without TUI messages)

type timelineItem struct {
	at        time.Duration
	isAssert  bool
	eventIdx  int
	assertIdx int
	condition string
}

func expandEvents(events []types.Event) []timelineItem {
	var items []timelineItem
	for i, e := range events {
		if e.Every.Duration > 0 {
			at := e.At.Duration
			count := 0
			for {
				if e.Count > 0 && count >= e.Count {
					break
				}
				if e.Until.Duration > 0 && at > e.Until.Duration {
					break
				}
				items = append(items, timelineItem{at: at, eventIdx: i, condition: e.If})
				count++
				at += e.Every.Duration
			}
		} else {
			items = append(items, timelineItem{at: e.At.Duration, eventIdx: i, condition: e.If})
		}
	}
	return items
}

func executeTimeline(ctx context.Context, sc *types.Scenario, exec *chaos.Executor, checker *assert.Checker, col *metrics.Collector, dc *docker.Client) error {
	items := expandEvents(sc.Events)
	for i, a := range sc.Assertions {
		items = append(items, timelineItem{at: a.At.Duration, isAssert: true, assertIdx: i})
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].at < items[j].at
	})

	start := time.Now()

	i := 0
	for i < len(items) {
		groupStart := i
		groupAt := items[i].at
		for i < len(items) && items[i].at == groupAt {
			i++
		}
		group := items[groupStart:i]

		waitDur := groupAt - time.Since(start)
		if waitDur > 0 {
			select {
			case <-time.After(waitDur):
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		if len(group) == 1 {
			runItem(ctx, sc, group[0], exec, checker, col, dc)
		} else {
			var wg sync.WaitGroup
			for _, item := range group {
				wg.Add(1)
				go func(item timelineItem) {
					defer wg.Done()
					runItem(ctx, sc, item, exec, checker, col, dc)
				}(item)
			}
			wg.Wait()
		}
	}

	return nil
}

func runItem(ctx context.Context, sc *types.Scenario, item timelineItem, exec *chaos.Executor, checker *assert.Checker, col *metrics.Collector, dc *docker.Client) {
	elapsed := formatDuration(item.at)

	if item.isAssert {
		a := sc.Assertions[item.assertIdx]
		result := checker.Check(ctx, a)
		col.RecordAssertion(result)
		if result.Success {
			fmt.Printf("  %s  assert:%-8s %-20s PASS  %s\n", elapsed, a.Type, a.Target, result.Message)
		} else {
			fmt.Printf("  %s  assert:%-8s %-20s FAIL  %s\n", elapsed, a.Type, a.Target, result.Message)
		}
		return
	}

	event := sc.Events[item.eventIdx]

	if item.condition != "" {
		met, err := evaluateCondition(ctx, item.condition, dc)
		if err != nil {
			fmt.Printf("  %s  %-14s %-20s FAIL  condition error: %s\n", elapsed, event.Action, event.Target, err)
			return
		}
		if !met {
			col.RecordEvent(event.Action, event.Target, nil)
			fmt.Printf("  %s  %-14s %-20s SKIP  condition not met\n", elapsed, event.Action, event.Target)
			return
		}
	}

	err := exec.Run(ctx, event.Action, event.Target, event.Params)
	col.RecordEvent(event.Action, event.Target, err)
	if err != nil {
		fmt.Printf("  %s  %-14s %-20s FAIL  %s\n", elapsed, event.Action, event.Target, err)
	} else {
		fmt.Printf("  %s  %-14s %-20s OK\n", elapsed, event.Action, event.Target)
	}
}

func evaluateCondition(ctx context.Context, condition string, dc *docker.Client) (bool, error) {
	condition = strings.TrimSpace(condition)
	match := conditionPattern.FindStringSubmatch(condition)
	if match == nil {
		return false, fmt.Errorf("unsupported condition: %q", condition)
	}
	nodeName := match[1]
	expectedState := match[2]
	nodes, err := dc.ListNodes(ctx)
	if err != nil {
		return false, err
	}
	for _, n := range nodes {
		if n.Name == nodeName {
			return n.State == expectedState, nil
		}
	}
	return false, fmt.Errorf("node %q not found", nodeName)
}

func injectBinary(ctx context.Context, dc *docker.Client, nodeNames []string, bin *types.CustomBinary) error {
	absPath, err := filepath.Abs(bin.Path)
	if err != nil {
		return fmt.Errorf("resolving binary path: %w", err)
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

func formatDuration(d time.Duration) string {
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%d:%02d", m, s)
}
