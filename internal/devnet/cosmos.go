package devnet

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tijnverbeek2004/nodetester/internal/docker"
	"github.com/tijnverbeek2004/nodetester/pkg/types"
)

const DefaultCosmosImage = "ghcr.io/cosmos/simapp:latest"

// CosmosDevnet orchestrates a Cosmos SDK localnet using simd (simapp).
type CosmosDevnet struct {
	docker      *docker.Client
	networkName string
	config      types.CosmosConfig
	nodes       []string
	status      func(string)
}

func NewCosmosDevnet(dc *docker.Client, networkName string, cfg types.CosmosConfig) *CosmosDevnet {
	if cfg.ChainID == "" {
		cfg.ChainID = "nodetester-1"
	}
	if cfg.BlockTime == "" {
		cfg.BlockTime = "1s"
	}
	return &CosmosDevnet{
		docker:      dc,
		networkName: networkName,
		config:      cfg,
	}
}

func (c *CosmosDevnet) emit(msg string) {
	if c.status != nil {
		c.status(msg)
	}
}

// Setup initializes the Cosmos localnet: inits chain, creates gentx, starts nodes.
func (c *CosmosDevnet) Setup(ctx context.Context, nodeNames []string, statusFn func(string)) error {
	c.status = statusFn
	c.nodes = nodeNames

	first := c.nodes[0]

	c.emit("initializing chain...")
	if err := c.initChain(ctx, first); err != nil {
		return fmt.Errorf("init chain: %w", err)
	}

	c.emit("configuring genesis...")
	if err := c.configureGenesis(ctx, first); err != nil {
		return fmt.Errorf("configuring genesis: %w", err)
	}

	if len(c.nodes) > 1 {
		c.emit("distributing genesis to peers...")
		if err := c.distributeGenesis(ctx, first); err != nil {
			return fmt.Errorf("distributing genesis: %w", err)
		}
	}

	c.emit("starting simd nodes...")
	if err := c.startNodes(ctx); err != nil {
		return fmt.Errorf("starting nodes: %w", err)
	}

	if len(c.nodes) > 1 {
		c.emit("connecting peers...")
		if err := c.connectPeers(ctx); err != nil {
			return fmt.Errorf("connecting peers: %w", err)
		}
	}

	return nil
}

func (c *CosmosDevnet) initChain(ctx context.Context, name string) error {
	// Initialize the chain with simd
	_, err := c.docker.Exec(ctx, name, []string{
		"simd", "init", "node-1",
		"--chain-id", c.config.ChainID,
		"--home", "/data",
	})
	if err != nil {
		return fmt.Errorf("simd init on %s: %w", name, err)
	}

	// Create a key for the validator
	_, err = c.docker.Exec(ctx, name, []string{
		"simd", "keys", "add", "validator",
		"--keyring-backend", "test",
		"--home", "/data",
	})
	if err != nil {
		return fmt.Errorf("creating key on %s: %w", name, err)
	}

	return nil
}

func (c *CosmosDevnet) configureGenesis(ctx context.Context, name string) error {
	// Add genesis account with tokens
	_, err := c.docker.Exec(ctx, name, []string{
		"simd", "genesis", "add-genesis-account", "validator",
		"100000000stake,100000000token",
		"--keyring-backend", "test",
		"--home", "/data",
	})
	if err != nil {
		return fmt.Errorf("adding genesis account on %s: %w", name, err)
	}

	// Create gentx
	_, err = c.docker.Exec(ctx, name, []string{
		"simd", "genesis", "gentx", "validator",
		"50000000stake",
		"--chain-id", c.config.ChainID,
		"--keyring-backend", "test",
		"--home", "/data",
	})
	if err != nil {
		return fmt.Errorf("gentx on %s: %w", name, err)
	}

	// Collect gentxs
	_, err = c.docker.Exec(ctx, name, []string{
		"simd", "genesis", "collect-gentxs",
		"--home", "/data",
	})
	if err != nil {
		return fmt.Errorf("collect-gentxs on %s: %w", name, err)
	}

	// Set block time in config.toml
	_, err = c.docker.Exec(ctx, name, []string{
		"sh", "-c", fmt.Sprintf(
			"sed -i 's/timeout_commit = .*/timeout_commit = \"%s\"/' /data/config/config.toml", c.config.BlockTime),
	})
	if err != nil {
		return fmt.Errorf("setting block time on %s: %w", name, err)
	}

	// Enable API
	_, _ = c.docker.Exec(ctx, name, []string{
		"sh", "-c", "sed -i 's/enable = false/enable = true/' /data/config/app.toml",
	})

	// Set listen address to 0.0.0.0
	_, _ = c.docker.Exec(ctx, name, []string{
		"sh", "-c", "sed -i 's/laddr = \"tcp:\\/\\/127.0.0.1:26657\"/laddr = \"tcp:\\/\\/0.0.0.0:26657\"/' /data/config/config.toml",
	})

	return nil
}

func (c *CosmosDevnet) distributeGenesis(ctx context.Context, first string) error {
	// Read genesis from first node
	genesis, err := c.docker.Exec(ctx, first, []string{"cat", "/data/config/genesis.json"})
	if err != nil {
		return fmt.Errorf("reading genesis: %w", err)
	}

	// Get node ID and IP for persistent peers
	nodeID, err := c.docker.Exec(ctx, first, []string{"simd", "comet", "show-node-id", "--home", "/data"})
	if err != nil {
		return fmt.Errorf("getting node ID: %w", err)
	}
	nodeID = strings.TrimSpace(nodeID)
	firstIP, err := c.docker.GetContainerIP(ctx, first, c.networkName)
	if err != nil {
		return fmt.Errorf("getting IP for %s: %w", first, err)
	}
	persistentPeer := fmt.Sprintf("%s@%s:26656", nodeID, firstIP)

	// Copy genesis and configure each other node
	for i, name := range c.nodes[1:] {
		moniker := fmt.Sprintf("node-%d", i+2)
		// Init node
		_, err := c.docker.Exec(ctx, name, []string{
			"simd", "init", moniker,
			"--chain-id", c.config.ChainID,
			"--home", "/data",
		})
		if err != nil {
			return fmt.Errorf("simd init on %s: %w", name, err)
		}

		// Copy genesis
		if err := c.docker.CopyToContainer(ctx, name, "/data/config", "genesis.json", []byte(genesis), 0644); err != nil {
			return fmt.Errorf("copying genesis to %s: %w", name, err)
		}

		// Set persistent peers
		_, _ = c.docker.Exec(ctx, name, []string{
			"sh", "-c", fmt.Sprintf(
				"sed -i 's/persistent_peers = .*/persistent_peers = \"%s\"/' /data/config/config.toml", persistentPeer),
		})

		// Set listen address
		_, _ = c.docker.Exec(ctx, name, []string{
			"sh", "-c", "sed -i 's/laddr = \"tcp:\\/\\/127.0.0.1:26657\"/laddr = \"tcp:\\/\\/0.0.0.0:26657\"/' /data/config/config.toml",
		})
	}

	return nil
}

func (c *CosmosDevnet) startNodes(ctx context.Context) error {
	for _, name := range c.nodes {
		cmd := "simd start --home /data > /data/simd.log 2>&1 &"
		if _, err := c.docker.Exec(ctx, name, []string{"sh", "-c", cmd}); err != nil {
			return fmt.Errorf("starting simd on %s: %w", name, err)
		}
	}
	return nil
}

func (c *CosmosDevnet) connectPeers(ctx context.Context) error {
	// Peers are configured via persistent_peers in config.toml
	// Just wait a moment for nodes to discover each other
	time.Sleep(2 * time.Second)
	return nil
}

// WaitForBlocks waits until the chain produces at least one block.
func (c *CosmosDevnet) WaitForBlocks(ctx context.Context) error {
	deadline := time.After(60 * time.Second)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("timeout waiting for cosmos block production")
		case <-ticker.C:
			output, err := c.docker.Exec(ctx, c.nodes[0], []string{
				"sh", "-c", "simd query block --home /data 2>/dev/null | head -1",
			})
			if err != nil {
				continue
			}
			if strings.Contains(output, "block") || strings.Contains(output, "height") {
				return nil
			}
		}
	}
}
