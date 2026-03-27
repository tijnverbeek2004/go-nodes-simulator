package chaos

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tijnverbeek2004/nodetester/internal/docker"
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
		case "loss":
			actionErr = e.loss(ctx, t, params)
		case "corrupt":
			actionErr = e.corrupt(ctx, t, params)
		case "reorder":
			actionErr = e.reorder(ctx, t, params)
		case "duplicate":
			actionErr = e.duplicate(ctx, t, params)
		case "stress":
			actionErr = e.stress(ctx, t, params)
		case "dns-fail":
			actionErr = e.dnsFail(ctx, t)
		case "dns-restore":
			actionErr = e.dnsRestore(ctx, t)
		case "bandwidth":
			actionErr = e.bandwidth(ctx, t, params)
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

// ensureIproute2 installs iproute2 (for tc) inside the container, skipping if already present.
func (e *Executor) ensureIproute2(ctx context.Context, target string) error {
	_, err := e.docker.Exec(ctx, target, []string{"sh", "-c",
		"which tc >/dev/null 2>&1 || apk add --no-cache iproute2 2>/dev/null || apt-get update && apt-get install -y iproute2 2>/dev/null || true"})
	if err != nil {
		return fmt.Errorf("installing iproute2: %w", err)
	}
	return nil
}

// applyNetem applies a tc netem rule. It tries "add" first, falls back to "change"
// if a qdisc already exists. Note: change replaces the entire netem configuration.
func (e *Executor) applyNetem(ctx context.Context, target string, netemArgs ...string) error {
	if err := e.ensureIproute2(ctx, target); err != nil {
		return err
	}

	addCmd := append([]string{"tc", "qdisc", "add", "dev", "eth0", "root", "netem"}, netemArgs...)
	_, err := e.docker.Exec(ctx, target, addCmd)
	if err != nil {
		changeCmd := append([]string{"tc", "qdisc", "change", "dev", "eth0", "root", "netem"}, netemArgs...)
		_, err = e.docker.Exec(ctx, target, changeCmd)
		if err != nil {
			return fmt.Errorf("applying tc netem: %w", err)
		}
	}
	return nil
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
	// Support optional jitter: delay Xms Yms (uniform distribution ±Y around X)
	args := []string{"delay", fmt.Sprintf("%dms", ms)}
	if jitter, ok := params["jitter"]; ok {
		j, err := strconv.Atoi(jitter)
		if err != nil || j < 0 {
			return fmt.Errorf("invalid jitter value: %q", jitter)
		}
		args = append(args, fmt.Sprintf("%dms", j))
	}
	return e.applyNetem(ctx, target, args...)
}

func (e *Executor) loss(ctx context.Context, target string, params map[string]string) error {
	pctStr, ok := params["percent"]
	if !ok {
		return fmt.Errorf("loss action requires 'percent' parameter")
	}
	pct, err := strconv.ParseFloat(pctStr, 64)
	if err != nil || pct <= 0 || pct > 100 {
		return fmt.Errorf("invalid loss percent: %q (must be 0-100)", pctStr)
	}
	return e.applyNetem(ctx, target, "loss", fmt.Sprintf("%.1f%%", pct))
}

func (e *Executor) corrupt(ctx context.Context, target string, params map[string]string) error {
	pctStr, ok := params["percent"]
	if !ok {
		return fmt.Errorf("corrupt action requires 'percent' parameter")
	}
	pct, err := strconv.ParseFloat(pctStr, 64)
	if err != nil || pct <= 0 || pct > 100 {
		return fmt.Errorf("invalid corrupt percent: %q (must be 0-100)", pctStr)
	}
	return e.applyNetem(ctx, target, "corrupt", fmt.Sprintf("%.1f%%", pct))
}

func (e *Executor) reorder(ctx context.Context, target string, params map[string]string) error {
	pctStr, ok := params["percent"]
	if !ok {
		return fmt.Errorf("reorder action requires 'percent' parameter")
	}
	pct, err := strconv.ParseFloat(pctStr, 64)
	if err != nil || pct <= 0 || pct > 100 {
		return fmt.Errorf("invalid reorder percent: %q (must be 0-100)", pctStr)
	}
	// reorder requires a base delay to have any observable effect
	delay := params["delay"]
	if delay == "" {
		delay = "10ms"
	}
	return e.applyNetem(ctx, target, "delay", delay, "reorder", fmt.Sprintf("%.1f%%", pct))
}

func (e *Executor) duplicate(ctx context.Context, target string, params map[string]string) error {
	pctStr, ok := params["percent"]
	if !ok {
		return fmt.Errorf("duplicate action requires 'percent' parameter")
	}
	pct, err := strconv.ParseFloat(pctStr, 64)
	if err != nil || pct <= 0 || pct > 100 {
		return fmt.Errorf("invalid duplicate percent: %q (must be 0-100)", pctStr)
	}
	return e.applyNetem(ctx, target, "duplicate", fmt.Sprintf("%.1f%%", pct))
}

func (e *Executor) stress(ctx context.Context, target string, params map[string]string) error {
	// Install stress-ng if not present (skip network fetch if already installed)
	if _, err := e.docker.Exec(ctx, target, []string{"sh", "-c",
		"which stress-ng >/dev/null 2>&1 || apk add --no-cache stress-ng 2>/dev/null || apt-get update && apt-get install -y stress-ng 2>/dev/null || true"}); err != nil {
		return fmt.Errorf("installing stress-ng: %w", err)
	}

	// Build stress-ng command from params
	args := []string{"stress-ng"}
	if cpu, ok := params["cpu"]; ok {
		args = append(args, "--cpu", cpu)
	}
	if vm, ok := params["vm"]; ok {
		args = append(args, "--vm", vm)
	}
	if vmBytes, ok := params["vm-bytes"]; ok {
		args = append(args, "--vm-bytes", vmBytes)
	}
	if io, ok := params["io"]; ok {
		args = append(args, "--io", io)
	}
	if hdd, ok := params["hdd"]; ok {
		args = append(args, "--hdd", hdd)
	}
	duration := params["duration"]
	if duration == "" {
		duration = "30s"
	}
	args = append(args, "--timeout", duration)

	if len(args) == 1 {
		return fmt.Errorf("stress action requires at least one stressor (cpu, vm, io, hdd)")
	}

	// Run in background so it doesn't block the event pipeline
	cmd := strings.Join(args, " ") + " &"
	if _, err := e.docker.Exec(ctx, target, []string{"sh", "-c", cmd}); err != nil {
		return fmt.Errorf("running stress-ng: %w", err)
	}
	return nil
}

func (e *Executor) dnsFail(ctx context.Context, target string) error {
	// Save resolv.conf contents to /tmp (can't cp Docker bind-mounts), then break DNS
	if _, err := e.docker.Exec(ctx, target, []string{"sh", "-c",
		"cat /etc/resolv.conf > /tmp/resolv.conf.nodetester.bak 2>/dev/null; echo 'nameserver 127.0.0.1' > /etc/resolv.conf"}); err != nil {
		return fmt.Errorf("breaking DNS: %w", err)
	}
	return nil
}

func (e *Executor) dnsRestore(ctx context.Context, target string) error {
	// Restore original resolv.conf contents from backup
	if _, err := e.docker.Exec(ctx, target, []string{"sh", "-c",
		"if [ -f /tmp/resolv.conf.nodetester.bak ]; then cat /tmp/resolv.conf.nodetester.bak > /etc/resolv.conf; fi"}); err != nil {
		return fmt.Errorf("restoring DNS: %w", err)
	}
	return nil
}

func (e *Executor) bandwidth(ctx context.Context, target string, params map[string]string) error {
	rate, ok := params["rate"]
	if !ok {
		return fmt.Errorf("bandwidth action requires 'rate' parameter (e.g. '1mbit', '500kbit')")
	}
	burst := params["burst"]
	if burst == "" {
		burst = "32kbit"
	}
	lat := params["latency"]
	if lat == "" {
		lat = "400ms"
	}

	if err := e.ensureIproute2(ctx, target); err != nil {
		return err
	}

	// Remove any existing netem qdisc first, then add tbf (token bucket filter)
	// tbf is a different qdisc than netem — they can't coexist on the same root
	_, _ = e.docker.Exec(ctx, target, []string{"tc", "qdisc", "del", "dev", "eth0", "root"})

	_, err := e.docker.Exec(ctx, target, []string{
		"tc", "qdisc", "add", "dev", "eth0", "root", "tbf",
		"rate", rate, "burst", burst, "latency", lat,
	})
	if err != nil {
		return fmt.Errorf("applying bandwidth limit: %w", err)
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
		"which iptables >/dev/null 2>&1 || apk add --no-cache iptables 2>/dev/null || apt-get update && apt-get install -y iptables 2>/dev/null || true"})
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
