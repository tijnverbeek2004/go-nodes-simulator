package metrics

import (
	"context"
	"sync"
	"time"

	"github.com/tijnverbeek2004/nodetester/internal/docker"
	"github.com/tijnverbeek2004/nodetester/pkg/types"
)

// StatsCollector polls container resource usage at a fixed interval.
type StatsCollector struct {
	docker    *docker.Client
	nodeNames []string
	interval  time.Duration

	mu        sync.Mutex
	history   []types.StatsSnapshot
	latest    map[string]types.ContainerStats
	onUpdate  func(map[string]types.ContainerStats) // callback on each poll
}

// NewStatsCollector creates a stats poller for the given nodes.
func NewStatsCollector(dc *docker.Client, nodeNames []string, interval time.Duration, onUpdate func(map[string]types.ContainerStats)) *StatsCollector {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	return &StatsCollector{
		docker:    dc,
		nodeNames: nodeNames,
		interval:  interval,
		latest:    make(map[string]types.ContainerStats),
		onUpdate:  onUpdate,
	}
}

// Start begins polling in a goroutine. Cancel the context to stop.
func (sc *StatsCollector) Start(ctx context.Context) {
	go sc.loop(ctx)
}

func (sc *StatsCollector) loop(ctx context.Context) {
	ticker := time.NewTicker(sc.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sc.poll(ctx)
		}
	}
}

func (sc *StatsCollector) poll(ctx context.Context) {
	snap := types.StatsSnapshot{
		Timestamp: time.Now(),
		Nodes:     make(map[string]types.ContainerStats),
	}

	for _, name := range sc.nodeNames {
		stats, err := sc.docker.ContainerStats(ctx, name)
		if err != nil {
			continue // node might be stopped
		}
		snap.Nodes[name] = *stats
	}

	if len(snap.Nodes) == 0 {
		return
	}

	sc.mu.Lock()
	sc.history = append(sc.history, snap)
	for k, v := range snap.Nodes {
		sc.latest[k] = v
	}
	sc.mu.Unlock()

	if sc.onUpdate != nil {
		sc.onUpdate(snap.Nodes)
	}
}

// Latest returns the most recent stats for each node.
func (sc *StatsCollector) Latest() map[string]types.ContainerStats {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	cp := make(map[string]types.ContainerStats, len(sc.latest))
	for k, v := range sc.latest {
		cp[k] = v
	}
	return cp
}

// History returns all recorded snapshots.
func (sc *StatsCollector) History() []types.StatsSnapshot {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	h := make([]types.StatsSnapshot, len(sc.history))
	copy(h, sc.history)
	return h
}
