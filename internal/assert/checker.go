package assert

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/tijnverbeek2004/nodetester/internal/docker"
	"github.com/tijnverbeek2004/nodetester/pkg/types"
)

// Checker evaluates assertions against running containers.
type Checker struct {
	docker      *docker.Client
	networkName string
}

// NewChecker creates an assertion checker.
func NewChecker(d *docker.Client, networkName string) *Checker {
	return &Checker{docker: d, networkName: networkName}
}

// Check evaluates a single assertion and returns the result.
func (c *Checker) Check(ctx context.Context, a types.Assertion) types.AssertionResult {
	result := types.AssertionResult{
		Timestamp: time.Now(),
		Type:      a.Type,
		Target:    a.Target,
	}

	switch a.Type {
	case "state":
		result = c.checkState(ctx, a, result)
	case "exec":
		result = c.checkExec(ctx, a, result)
	default:
		result.Success = false
		result.Message = fmt.Sprintf("unknown assertion type %q", a.Type)
	}

	return result
}

func (c *Checker) checkState(ctx context.Context, a types.Assertion, result types.AssertionResult) types.AssertionResult {
	targets, err := c.resolveTargets(ctx, a.Target)
	if err != nil {
		result.Success = false
		result.Message = fmt.Sprintf("resolving targets: %s", err)
		return result
	}
	if len(targets) == 0 {
		result.Success = false
		result.Message = fmt.Sprintf("no containers matched %q", a.Target)
		return result
	}

	nodes, err := c.docker.ListNodes(ctx)
	if err != nil {
		result.Success = false
		result.Message = fmt.Sprintf("listing nodes: %s", err)
		return result
	}

	nodeMap := make(map[string]string)
	for _, n := range nodes {
		nodeMap[n.Name] = n.State
	}

	var failures []string
	for _, t := range targets {
		state, ok := nodeMap[t]
		if !ok {
			failures = append(failures, fmt.Sprintf("%s: not found", t))
			continue
		}
		if state != a.Expect {
			failures = append(failures, fmt.Sprintf("%s: %s (expected %s)", t, state, a.Expect))
		}
	}

	if len(failures) == 0 {
		result.Success = true
		if len(targets) == 1 {
			result.Message = fmt.Sprintf("%s is %s", targets[0], a.Expect)
		} else {
			result.Message = fmt.Sprintf("all %d nodes are %s", len(targets), a.Expect)
		}
	} else {
		result.Success = false
		result.Message = strings.Join(failures, "; ")
	}

	return result
}

func (c *Checker) checkExec(ctx context.Context, a types.Assertion, result types.AssertionResult) types.AssertionResult {
	targets, err := c.resolveTargets(ctx, a.Target)
	if err != nil {
		result.Success = false
		result.Message = fmt.Sprintf("resolving targets: %s", err)
		return result
	}
	if len(targets) == 0 {
		result.Success = false
		result.Message = fmt.Sprintf("no containers matched %q", a.Target)
		return result
	}

	expect := a.Expect
	if expect == "" {
		expect = "success"
	}

	var failures []string
	for _, t := range targets {
		output, execErr := c.docker.Exec(ctx, t, []string{"sh", "-c", a.Command})

		if expect == "success" && execErr != nil {
			failures = append(failures, fmt.Sprintf("%s: command failed: %s", t, execErr))
			continue
		}
		if expect == "failure" && execErr == nil {
			failures = append(failures, fmt.Sprintf("%s: expected failure but command succeeded", t))
			continue
		}

		if a.Contains != "" && !strings.Contains(output, a.Contains) {
			failures = append(failures, fmt.Sprintf("%s: output missing %q", t, a.Contains))
		}
	}

	if len(failures) == 0 {
		result.Success = true
		if a.Contains != "" {
			result.Message = fmt.Sprintf("output contains %q", a.Contains)
		} else {
			result.Message = fmt.Sprintf("command %s as expected", expect)
		}
	} else {
		result.Success = false
		result.Message = strings.Join(failures, "; ")
	}

	return result
}

func (c *Checker) resolveTargets(ctx context.Context, pattern string) ([]string, error) {
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

		nodes, err := c.docker.ListNodes(ctx)
		if err != nil {
			return nil, fmt.Errorf("listing nodes: %w", err)
		}
		for _, n := range nodes {
			ok, err := filepath.Match(p, n.Name)
			if err != nil {
				return nil, fmt.Errorf("invalid pattern %q: %w", p, err)
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
