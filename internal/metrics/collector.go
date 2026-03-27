package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/tijnverbeek2004/nodetester/internal/docker"
	"github.com/tijnverbeek2004/nodetester/pkg/types"
)

// Collector gathers container status and records chaos events and assertion results.
type Collector struct {
	docker     *docker.Client
	mu         sync.Mutex
	events     []types.EventRecord
	assertions []types.AssertionResult
}

// NewCollector creates a metrics collector.
func NewCollector(d *docker.Client) *Collector {
	return &Collector{docker: d}
}

// RecordEvent logs a chaos event that was executed.
func (c *Collector) RecordEvent(action, target string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	record := types.EventRecord{
		Timestamp: time.Now(),
		Action:    action,
		Target:    target,
		Success:   err == nil,
	}
	if err != nil {
		record.Error = err.Error()
	}
	c.events = append(c.events, record)
}

// RecordAssertion logs an assertion result.
func (c *Collector) RecordAssertion(result types.AssertionResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.assertions = append(c.assertions, result)
}

// Assertions returns a copy of all recorded assertion results.
func (c *Collector) Assertions() []types.AssertionResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	results := make([]types.AssertionResult, len(c.assertions))
	copy(results, c.assertions)
	return results
}

// Snapshot polls Docker and returns current status of all nodetester nodes.
func (c *Collector) Snapshot(ctx context.Context) ([]types.NodeStatus, error) {
	nodes, err := c.docker.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	for i := range nodes {
		nodes[i].LastChecked = now
	}
	return nodes, nil
}

// Report holds the final output of a scenario run.
type Report struct {
	Nodes      []types.NodeStatus      `json:"nodes"`
	Events     []types.EventRecord     `json:"events"`
	Assertions []types.AssertionResult  `json:"assertions,omitempty"`
}

// WriteReport writes a JSON report to the given file path.
func (c *Collector) WriteReport(ctx context.Context, path string) error {
	nodes, err := c.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("collecting final snapshot: %w", err)
	}

	c.mu.Lock()
	events := make([]types.EventRecord, len(c.events))
	copy(events, c.events)
	c.mu.Unlock()

	assertions := make([]types.AssertionResult, len(c.assertions))
	copy(assertions, c.assertions)

	report := Report{
		Nodes:      nodes,
		Events:     events,
		Assertions: assertions,
	}

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling report: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("writing report file: %w", err)
	}

	return nil
}

// Events returns a copy of all recorded event records.
func (c *Collector) Events() []types.EventRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	events := make([]types.EventRecord, len(c.events))
	copy(events, c.events)
	return events
}
