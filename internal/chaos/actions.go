package chaos

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tijn/nodetester/internal/docker"
)

// Executor runs chaos actions against real containers.
type Executor struct {
	docker      *docker.Client
	networkName string
}

// NewExecutor creates a chaos executor backed by a Docker client.
func NewExecutor(d *docker.Client, networkName string) *Executor {
	return &Executor{docker: d, networkName: networkName}
}

// Run dispatches a chaos action to the appropriate handler.
// If the target contains a wildcard (e.g. "node-*"), it resolves to all
// matching containers and runs the action on each one.
func (e *Executor) Run(ctx context.Context, action, target string, params map[string]string) error {
	// Partition and heal are special — they operate on two groups,
	// so we handle them before the standard per-target loop.
	if action == "partition" {
		return e.partition(ctx, target, params)
	}
	if action == "heal" {
		return e.heal(ctx, target)
	}

	targets, err := e.resolveTargets(ctx, target)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return fmt.Errorf("no containers matched target %q", target)
	}

	for _, t := range targets {
		var actionErr error
		switch action {
		case "stop":
			actionErr = e.stop(ctx, t)
		case "restart":
			actionErr = e.restart(ctx, t)
		case "latency":
			actionErr = e.latency(ctx, t, params)
		default:
			actionErr = fmt.Errorf("action %q not yet implemented", action)
		}
		if actionErr != nil {
			return fmt.Errorf("%s on %s: %w", action, t, actionErr)
		}
	}
	return nil
}

// resolveTargets expands a target pattern to a list of container names.
// Supports exact names ("node-2"), glob patterns ("node-*"),
// and comma-separated lists ("node-1,node-2").
func (e *Executor) resolveTargets(ctx context.Context, pattern string) ([]string, error) {
	// Split on commas to support "node-1,node-2" syntax.
	parts := strings.Split(pattern, ",")
	var allMatched []string

	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}

		if !hasGlobChar(p) {
			allMatched = append(allMatched, p)
			continue
		}

		nodes, err := e.docker.ListNodes(ctx)
		if err != nil {
			return nil, fmt.Errorf("listing nodes for target resolution: %w", err)
		}
		for _, n := range nodes {
			ok, err := filepath.Match(p, n.Name)
			if err != nil {
				return nil, fmt.Errorf("invalid target pattern %q: %w", p, err)
			}
			if ok {
				allMatched = append(allMatched, n.Name)
			}
		}
	}

	return allMatched, nil
}

func hasGlobChar(s string) bool {
	for _, c := range s {
		if c == '*' || c == '?' || c == '[' {
			return true
		}
	}
	return false
}

func (e *Executor) stop(ctx context.Context, target string) error {
	fmt.Printf("  [chaos] stopping %s\n", target)
	return e.docker.StopNode(ctx, target)
}

func (e *Executor) restart(ctx context.Context, target string) error {
	fmt.Printf("  [chaos] restarting %s\n", target)
	return e.docker.RestartNode(ctx, target)
}

// latency injects network delay using tc netem.
func (e *Executor) latency(ctx context.Context, target string, params map[string]string) error {
	msStr, ok := params["ms"]
	if !ok {
		return fmt.Errorf("latency action requires 'ms' parameter")
	}
	ms, err := strconv.Atoi(msStr)
	if err != nil || ms <= 0 {
		return fmt.Errorf("invalid latency ms value: %q", msStr)
	}

	fmt.Printf("  [chaos] injecting %dms latency on %s\n", ms, target)

	// Install iproute2 (provides tc). Tries Alpine apk first, then Debian apt-get.
	fmt.Printf("  [chaos] ensuring iproute2 is installed on %s\n", target)
	if _, err := e.docker.Exec(ctx, target, []string{"sh", "-c", "apk add --no-cache iproute2 2>/dev/null || apt-get update && apt-get install -y iproute2 2>/dev/null || true"}); err != nil {
		return fmt.Errorf("installing iproute2: %w", err)
	}

	// Try to add a netem qdisc. If one already exists, replace it instead.
	delay := fmt.Sprintf("%dms", ms)
	_, err = e.docker.Exec(ctx, target, []string{"tc", "qdisc", "add", "dev", "eth0", "root", "netem", "delay", delay})
	if err != nil {
		_, err = e.docker.Exec(ctx, target, []string{"tc", "qdisc", "change", "dev", "eth0", "root", "netem", "delay", delay})
		if err != nil {
			return fmt.Errorf("applying tc netem: %w", err)
		}
	}

	fmt.Printf("  [chaos] %s now has %s delay on eth0\n", target, delay)
	return nil
}

// partition creates a bidirectional network partition using iptables.
// target = comma/glob list of group A nodes.
// params["from"] = comma/glob list of group B nodes.
// Each node in group A gets iptables DROP rules for every IP in group B, and vice versa.
func (e *Executor) partition(ctx context.Context, target string, params map[string]string) error {
	fromPattern, ok := params["from"]
	if !ok || fromPattern == "" {
		return fmt.Errorf("partition action requires 'from' parameter")
	}

	groupA, err := e.resolveTargets(ctx, target)
	if err != nil {
		return fmt.Errorf("resolving partition group A: %w", err)
	}
	groupB, err := e.resolveTargets(ctx, fromPattern)
	if err != nil {
		return fmt.Errorf("resolving partition group B: %w", err)
	}

	if len(groupA) == 0 {
		return fmt.Errorf("partition group A matched no containers (target=%q)", target)
	}
	if len(groupB) == 0 {
		return fmt.Errorf("partition group B matched no containers (from=%q)", fromPattern)
	}

	fmt.Printf("  [chaos] partitioning [%s] from [%s]\n",
		strings.Join(groupA, ", "), strings.Join(groupB, ", "))

	// Look up IPs for all nodes in both groups.
	ipsA, err := e.resolveIPs(ctx, groupA)
	if err != nil {
		return err
	}
	ipsB, err := e.resolveIPs(ctx, groupB)
	if err != nil {
		return err
	}

	// Install iptables on all affected nodes.
	allNodes := append(groupA, groupB...)
	for _, node := range allNodes {
		if err := e.ensureIptables(ctx, node); err != nil {
			return err
		}
	}

	// For each node in group A, block all IPs from group B.
	for _, node := range groupA {
		for _, ip := range ipsB {
			if err := e.blockIP(ctx, node, ip); err != nil {
				return err
			}
		}
	}

	// For each node in group B, block all IPs from group A.
	for _, node := range groupB {
		for _, ip := range ipsA {
			if err := e.blockIP(ctx, node, ip); err != nil {
				return err
			}
		}
	}

	fmt.Printf("  [chaos] partition active: %d nodes isolated from %d nodes\n", len(groupA), len(groupB))
	return nil
}

// heal removes all iptables rules added by partition, restoring connectivity.
// target supports the same comma/glob syntax — applies to all matched nodes.
func (e *Executor) heal(ctx context.Context, target string) error {
	targets, err := e.resolveTargets(ctx, target)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return fmt.Errorf("no containers matched target %q for heal", target)
	}

	fmt.Printf("  [chaos] healing partition on [%s]\n", strings.Join(targets, ", "))

	for _, node := range targets {
		// Flush the NODETESTER chain — removes all our drop rules.
		// If the chain doesn't exist, that's fine — partition was never applied.
		_, err := e.docker.Exec(ctx, node, []string{"sh", "-c",
			"iptables -F NODETESTER 2>/dev/null; iptables -D INPUT -j NODETESTER 2>/dev/null; iptables -D OUTPUT -j NODETESTER 2>/dev/null; iptables -X NODETESTER 2>/dev/null; true"})
		if err != nil {
			fmt.Printf("  [chaos] WARNING: heal on %s had errors: %v\n", node, err)
		}
	}

	fmt.Println("  [chaos] partition healed")
	return nil
}

// resolveIPs looks up the container IP for each node on the scenario network.
func (e *Executor) resolveIPs(ctx context.Context, names []string) ([]string, error) {
	ips := make([]string, 0, len(names))
	for _, name := range names {
		ip, err := e.docker.GetContainerIP(ctx, name, e.networkName)
		if err != nil {
			return nil, fmt.Errorf("getting IP for %s: %w", name, err)
		}
		fmt.Printf("  [chaos] resolved %s -> %s\n", name, ip)
		ips = append(ips, ip)
	}
	return ips, nil
}

// ensureIptables installs iptables inside a container (Alpine or Debian).
func (e *Executor) ensureIptables(ctx context.Context, node string) error {
	_, err := e.docker.Exec(ctx, node, []string{"sh", "-c",
		"apk add --no-cache iptables 2>/dev/null || apt-get update && apt-get install -y iptables 2>/dev/null || true"})
	if err != nil {
		return fmt.Errorf("installing iptables on %s: %w", node, err)
	}
	return nil
}

// blockIP adds iptables rules to drop all traffic to/from a specific IP.
// Uses a dedicated NODETESTER chain so we can flush it cleanly during heal.
func (e *Executor) blockIP(ctx context.Context, node, ip string) error {
	// Create our chain if it doesn't exist, and jump to it from INPUT/OUTPUT.
	// The "2>/dev/null || true" makes this idempotent.
	setup := fmt.Sprintf(
		"iptables -N NODETESTER 2>/dev/null || true; "+
			"iptables -C INPUT -j NODETESTER 2>/dev/null || iptables -A INPUT -j NODETESTER; "+
			"iptables -C OUTPUT -j NODETESTER 2>/dev/null || iptables -A OUTPUT -j NODETESTER",
	)
	if _, err := e.docker.Exec(ctx, node, []string{"sh", "-c", setup}); err != nil {
		return fmt.Errorf("setting up iptables chain on %s: %w", node, err)
	}

	// Drop incoming and outgoing traffic for this IP.
	drop := fmt.Sprintf(
		"iptables -A NODETESTER -s %s -j DROP; iptables -A NODETESTER -d %s -j DROP", ip, ip,
	)
	if _, err := e.docker.Exec(ctx, node, []string{"sh", "-c", drop}); err != nil {
		return fmt.Errorf("blocking %s on %s: %w", ip, node, err)
	}

	fmt.Printf("  [chaos] %s: blocked traffic to/from %s\n", node, ip)
	return nil
}
