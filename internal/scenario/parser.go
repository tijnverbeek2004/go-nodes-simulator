package scenario

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/tijnverbeek2004/nodetester/pkg/types"
)

// Load reads and validates a scenario YAML file.
func Load(path string) (*types.Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading scenario file: %w", err)
	}

	var s types.Scenario
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parsing scenario YAML: %w", err)
	}

	if err := validate(&s); err != nil {
		return nil, fmt.Errorf("invalid scenario: %w", err)
	}

	return &s, nil
}

// validate checks that the scenario has sane values.
func validate(s *types.Scenario) error {
	// If preset is "ethereum", image is optional (defaults to geth image).
	if s.Nodes.Preset == "ethereum" {
		if s.Nodes.Ethereum == nil {
			s.Nodes.Ethereum = &types.EthereumConfig{}
		}
	} else if s.Nodes.Preset != "" {
		return fmt.Errorf("unknown preset %q (supported: ethereum)", s.Nodes.Preset)
	}

	if s.Nodes.Image == "" && s.Nodes.Preset == "" {
		return fmt.Errorf("nodes.image is required (or use preset: ethereum)")
	}

	if s.Nodes.Binary != nil && s.Nodes.Binary.Path == "" {
		return fmt.Errorf("binary.path is required when binary is specified")
	}

	if s.Nodes.Count < 1 {
		return fmt.Errorf("nodes.count must be at least 1")
	}
	if s.Nodes.Count > 50 {
		return fmt.Errorf("nodes.count exceeds maximum of 50")
	}

	validActions := map[string]bool{
		"stop":        true,
		"restart":     true,
		"latency":     true,
		"partition":   true,
		"heal":        true,
		"loss":        true,
		"corrupt":     true,
		"reorder":     true,
		"duplicate":   true,
		"stress":      true,
		"dns-fail":    true,
		"dns-restore": true,
		"bandwidth":   true,
		"http":        true,
	}

	// Actions that require a 'percent' parameter.
	percentActions := map[string]bool{
		"loss":      true,
		"corrupt":   true,
		"reorder":   true,
		"duplicate": true,
	}

	for i, e := range s.Events {
		if !validActions[e.Action] {
			return fmt.Errorf("event %d: unknown action %q", i, e.Action)
		}
		if e.Target == "" {
			return fmt.Errorf("event %d: target is required", i)
		}
		if e.At.Duration <= 0 {
			return fmt.Errorf("event %d: 'at' must be a positive duration", i)
		}
		if e.Action == "partition" {
			if e.Params["from"] == "" {
				return fmt.Errorf("event %d: partition requires 'from' parameter", i)
			}
		}
		if e.Action == "latency" {
			if e.Params["ms"] == "" {
				return fmt.Errorf("event %d: latency requires 'ms' parameter", i)
			}
		}
		if percentActions[e.Action] {
			if e.Params["percent"] == "" {
				return fmt.Errorf("event %d: %s requires 'percent' parameter", i, e.Action)
			}
		}
		if e.Action == "bandwidth" {
			if e.Params["rate"] == "" {
				return fmt.Errorf("event %d: bandwidth requires 'rate' parameter", i)
			}
		}
		if e.Action == "stress" {
			hasStressor := e.Params["cpu"] != "" || e.Params["vm"] != "" || e.Params["io"] != "" || e.Params["hdd"] != ""
			if !hasStressor {
				return fmt.Errorf("event %d: stress requires at least one stressor (cpu, vm, io, hdd)", i)
			}
		}
		// Validate repeat/loop fields
		if e.Every.Duration > 0 {
			if e.Count == 0 && e.Until.Duration == 0 {
				return fmt.Errorf("event %d: 'every' requires 'count' or 'until' to limit repetitions", i)
			}
			if e.Count < 0 {
				return fmt.Errorf("event %d: 'count' must be positive", i)
			}
			if e.Until.Duration > 0 && e.Until.Duration <= e.At.Duration {
				return fmt.Errorf("event %d: 'until' must be after 'at'", i)
			}
		} else {
			if e.Count > 0 || e.Until.Duration > 0 {
				return fmt.Errorf("event %d: 'count' and 'until' require 'every'", i)
			}
		}
	}

	// Validate assertions
	validAssertTypes := map[string]bool{
		"state": true,
		"exec":  true,
	}

	for i, a := range s.Assertions {
		if !validAssertTypes[a.Type] {
			return fmt.Errorf("assertion %d: unknown type %q (supported: state, exec)", i, a.Type)
		}
		if a.Target == "" {
			return fmt.Errorf("assertion %d: target is required", i)
		}
		if a.At.Duration <= 0 {
			return fmt.Errorf("assertion %d: 'at' must be a positive duration", i)
		}
		if a.Type == "state" {
			if a.Expect == "" {
				return fmt.Errorf("assertion %d: state assertion requires 'expect' (e.g. running, exited)", i)
			}
		}
		if a.Type == "exec" {
			if a.Command == "" {
				return fmt.Errorf("assertion %d: exec assertion requires 'command'", i)
			}
		}
	}

	return nil
}
