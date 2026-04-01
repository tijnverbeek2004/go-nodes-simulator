package devnet

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tijnverbeek2004/nodetester/internal/docker"
	"github.com/tijnverbeek2004/nodetester/pkg/types"
)

const DefaultSolanaImage = "solanalabs/solana:v1.18.26"

// SolanaDevnet orchestrates a Solana test-validator cluster.
type SolanaDevnet struct {
	docker      *docker.Client
	networkName string
	config      types.SolanaConfig
	nodes       []string
	status      func(string)
}

func NewSolanaDevnet(dc *docker.Client, networkName string, cfg types.SolanaConfig) *SolanaDevnet {
	if cfg.SlotsPerEpoch == 0 {
		cfg.SlotsPerEpoch = 50
	}
	return &SolanaDevnet{
		docker:      dc,
		networkName: networkName,
		config:      cfg,
	}
}

func (s *SolanaDevnet) emit(msg string) {
	if s.status != nil {
		s.status(msg)
	}
}

// Setup starts solana-test-validator on the first node.
// Additional nodes run as RPC clients connected to the validator.
func (s *SolanaDevnet) Setup(ctx context.Context, nodeNames []string, statusFn func(string)) error {
	s.status = statusFn
	s.nodes = nodeNames

	first := s.nodes[0]

	s.emit("starting solana-test-validator...")
	if err := s.startValidator(ctx, first); err != nil {
		return fmt.Errorf("starting validator: %w", err)
	}

	s.emit("waiting for validator to be ready...")
	if err := s.waitForValidator(ctx, first); err != nil {
		return err
	}

	// Additional nodes connect as RPC watchers
	if len(s.nodes) > 1 {
		s.emit("configuring RPC watchers...")
		firstIP, err := s.docker.GetContainerIP(ctx, first, s.networkName)
		if err != nil {
			return fmt.Errorf("getting IP for %s: %w", first, err)
		}
		for _, name := range s.nodes[1:] {
			// Configure CLI to point to the validator
			_, _ = s.docker.Exec(ctx, name, []string{
				"solana", "config", "set", "--url", fmt.Sprintf("http://%s:8899", firstIP),
			})
		}
	}

	return nil
}

func (s *SolanaDevnet) startValidator(ctx context.Context, name string) error {
	cmd := fmt.Sprintf(
		"solana-test-validator"+
			" --slots-per-epoch %d"+
			" --rpc-port 8899"+
			" --bind-address 0.0.0.0"+
			" --ledger /data/ledger"+
			" --log > /data/validator.log 2>&1 &",
		s.config.SlotsPerEpoch,
	)

	if _, err := s.docker.Exec(ctx, name, []string{"sh", "-c", "mkdir -p /data && " + cmd}); err != nil {
		return fmt.Errorf("starting validator on %s: %w", name, err)
	}
	return nil
}

func (s *SolanaDevnet) waitForValidator(ctx context.Context, name string) error {
	deadline := time.After(60 * time.Second)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("timeout waiting for solana-test-validator on %s", name)
		case <-ticker.C:
			output, err := s.docker.Exec(ctx, name, []string{
				"solana", "cluster-version", "--url", "http://127.0.0.1:8899",
			})
			if err == nil && strings.TrimSpace(output) != "" {
				return nil
			}
		}
	}
}

// WaitForBlocks waits until at least one slot has been processed.
func (s *SolanaDevnet) WaitForBlocks(ctx context.Context) error {
	deadline := time.After(60 * time.Second)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("timeout waiting for solana slot production")
		case <-ticker.C:
			output, err := s.docker.Exec(ctx, s.nodes[0], []string{
				"solana", "slot", "--url", "http://127.0.0.1:8899",
			})
			if err != nil {
				continue
			}
			slot := strings.TrimSpace(output)
			if slot != "0" && slot != "" {
				return nil
			}
		}
	}
}
