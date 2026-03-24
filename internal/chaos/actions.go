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
func (e *Executor) Run(ctx context.Context, action, target string, params map[string]string) error {
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
			actionErr = e.docker.StopNode(ctx, t)
		case "restart":
			actionErr = e.docker.RestartNode(ctx, t)
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

func (e *Executor) resolveTargets(ctx context.Context, pattern string) ([]string, error) {
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

func (e *Executor) latency(ctx context.Context, target string, params map[string]string) error {
	msStr, ok := params["ms"]
	if !ok {
		return fmt.Errorf("latency action requires 'ms' parameter")
	}
	ms, err := strconv.Atoi(msStr)
	if err != nil || ms <= 0 {
		return fmt.Errorf("invalid latency ms value: %q", msStr)
	}

	if _, err := e.docker.Exec(ctx, target, []string{"sh", "-c", "apk add --no-cache iproute2 2>/dev/null || apt-get update && apt-get install -y iproute2 2>/dev/null || true"}); err != nil {
		return fmt.Errorf("installing iproute2: %w", err)
	}

	delay := fmt.Sprintf("%dms", ms)
	_, err = e.docker.Exec(ctx, target, []string{"tc", "qdisc", "add", "dev", "eth0", "root", "netem", "delay", delay})
	if err != nil {
		_, err = e.docker.Exec(ctx, target, []string{"tc", "qdisc", "change", "dev", "eth0", "root", "netem", "delay", delay})
		if err != nil {
			return fmt.Errorf("applying tc netem: %w", err)
		}
	}

	return nil
}

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

	ipsA, err := e.resolveIPs(ctx, groupA)
	if err != nil {
		return err
	}
	ipsB, err := e.resolveIPs(ctx, groupB)
	if err != nil {
		return err
	}

	allNodes := append(groupA, groupB...)
	for _, node := range allNodes {
		if err := e.ensureIptables(ctx, node); err != nil {
			return err
		}
	}

	for _, node := range groupA {
		for _, ip := range ipsB {
			if err := e.blockIP(ctx, node, ip); err != nil {
				return err
			}
		}
	}

	for _, node := range groupB {
		for _, ip := range ipsA {
			if err := e.blockIP(ctx, node, ip); err != nil {
				return err
			}
		}
	}

	return nil
}

func (e *Executor) heal(ctx context.Context, target string) error {
	targets, err := e.resolveTargets(ctx, target)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return fmt.Errorf("no containers matched target %q for heal", target)
	}

	for _, node := range targets {
		_, _ = e.docker.Exec(ctx, node, []string{"sh", "-c",
			"iptables -F NODETESTER 2>/dev/null; iptables -D INPUT -j NODETESTER 2>/dev/null; iptables -D OUTPUT -j NODETESTER 2>/dev/null; iptables -X NODETESTER 2>/dev/null; true"})
	}

	return nil
}

func (e *Executor) resolveIPs(ctx context.Context, names []string) ([]string, error) {
	ips := make([]string, 0, len(names))
	for _, name := range names {
		ip, err := e.docker.GetContainerIP(ctx, name, e.networkName)
		if err != nil {
			return nil, fmt.Errorf("getting IP for %s: %w", name, err)
		}
		ips = append(ips, ip)
	}
	return ips, nil
}

func (e *Executor) ensureIptables(ctx context.Context, node string) error {
	_, err := e.docker.Exec(ctx, node, []string{"sh", "-c",
		"apk add --no-cache iptables 2>/dev/null || apt-get update && apt-get install -y iptables 2>/dev/null || true"})
	if err != nil {
		return fmt.Errorf("installing iptables on %s: %w", node, err)
	}
	return nil
}

func (e *Executor) blockIP(ctx context.Context, node, ip string) error {
	setup := fmt.Sprintf(
		"iptables -N NODETESTER 2>/dev/null || true; "+
			"iptables -C INPUT -j NODETESTER 2>/dev/null || iptables -A INPUT -j NODETESTER; "+
			"iptables -C OUTPUT -j NODETESTER 2>/dev/null || iptables -A OUTPUT -j NODETESTER",
	)
	if _, err := e.docker.Exec(ctx, node, []string{"sh", "-c", setup}); err != nil {
		return fmt.Errorf("setting up iptables chain on %s: %w", node, err)
	}

	drop := fmt.Sprintf(
		"iptables -A NODETESTER -s %s -j DROP; iptables -A NODETESTER -d %s -j DROP", ip, ip,
	)
	if _, err := e.docker.Exec(ctx, node, []string{"sh", "-c", drop}); err != nil {
		return fmt.Errorf("blocking %s on %s: %w", ip, node, err)
	}

	return nil
}
