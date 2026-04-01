package devnet

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tijnverbeek2004/nodetester/internal/docker"
	"github.com/tijnverbeek2004/nodetester/pkg/types"
)

const DefaultBitcoinImage = "ruimarinho/bitcoin-core:latest"

// BtcDevnet orchestrates a Bitcoin regtest network.
type BtcDevnet struct {
	docker      *docker.Client
	networkName string
	config      types.BitcoinConfig
	nodes       []string
	status      func(string)
}

func NewBtcDevnet(dc *docker.Client, networkName string, cfg types.BitcoinConfig) *BtcDevnet {
	if cfg.BlockTime == 0 {
		cfg.BlockTime = 10
	}
	return &BtcDevnet{
		docker:      dc,
		networkName: networkName,
		config:      cfg,
	}
}

func (b *BtcDevnet) emit(msg string) {
	if b.status != nil {
		b.status(msg)
	}
}

// Setup starts bitcoind in regtest mode on each node and connects peers.
func (b *BtcDevnet) Setup(ctx context.Context, nodeNames []string, statusFn func(string)) error {
	b.status = statusFn
	b.nodes = nodeNames

	b.emit("starting bitcoind in regtest mode...")
	if err := b.startNodes(ctx); err != nil {
		return err
	}

	b.emit("waiting for RPC ready...")
	if err := b.waitForRPC(ctx); err != nil {
		return err
	}

	b.emit("connecting peers...")
	if err := b.connectPeers(ctx); err != nil {
		return err
	}

	b.emit("creating wallet and generating initial blocks...")
	if err := b.generateBlocks(ctx); err != nil {
		return err
	}

	return nil
}

func (b *BtcDevnet) startNodes(ctx context.Context) error {
	for _, name := range b.nodes {
		cmd := "bitcoind -regtest -server -daemon" +
			" -rpcuser=nodetester -rpcpassword=nodetester" +
			" -rpcallowip=0.0.0.0/0 -rpcbind=0.0.0.0" +
			" -listen=1 -port=18444" +
			" -fallbackfee=0.0001" +
			" -datadir=/data" +
			" -printtoconsole=0"

		if _, err := b.docker.Exec(ctx, name, []string{"sh", "-c", "mkdir -p /data && " + cmd}); err != nil {
			return fmt.Errorf("starting bitcoind on %s: %w", name, err)
		}
	}
	return nil
}

func (b *BtcDevnet) waitForRPC(ctx context.Context) error {
	for _, name := range b.nodes {
		if err := b.waitForNodeRPC(ctx, name); err != nil {
			return err
		}
	}
	return nil
}

func (b *BtcDevnet) waitForNodeRPC(ctx context.Context, name string) error {
	deadline := time.After(30 * time.Second)
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("timeout waiting for bitcoind RPC on %s", name)
		case <-ticker.C:
			_, err := b.docker.Exec(ctx, name, []string{
				"bitcoin-cli", "-regtest",
				"-rpcuser=nodetester", "-rpcpassword=nodetester",
				"getblockchaininfo",
			})
			if err == nil {
				return nil
			}
		}
	}
}

func (b *BtcDevnet) connectPeers(ctx context.Context) error {
	// Get IPs for all nodes
	ips := make(map[string]string)
	for _, name := range b.nodes {
		ip, err := b.docker.GetContainerIP(ctx, name, b.networkName)
		if err != nil {
			return fmt.Errorf("getting IP for %s: %w", name, err)
		}
		ips[name] = ip
	}

	// Connect each node to all other nodes
	for _, name := range b.nodes {
		for _, other := range b.nodes {
			if name == other {
				continue
			}
			otherIP := ips[other]
			_, _ = b.docker.Exec(ctx, name, []string{
				"bitcoin-cli", "-regtest",
				"-rpcuser=nodetester", "-rpcpassword=nodetester",
				"addnode", fmt.Sprintf("%s:18444", otherIP), "add",
			})
		}
	}
	return nil
}

func (b *BtcDevnet) generateBlocks(ctx context.Context) error {
	// Create wallet on first node and generate initial blocks
	first := b.nodes[0]
	_, _ = b.docker.Exec(ctx, first, []string{
		"bitcoin-cli", "-regtest",
		"-rpcuser=nodetester", "-rpcpassword=nodetester",
		"createwallet", "default",
	})

	// Get a new address
	addr, err := b.docker.Exec(ctx, first, []string{
		"bitcoin-cli", "-regtest",
		"-rpcuser=nodetester", "-rpcpassword=nodetester",
		"getnewaddress",
	})
	if err != nil {
		return fmt.Errorf("getting address: %w", err)
	}
	addr = strings.TrimSpace(addr)

	// Generate 101 blocks (100 to mature coinbase + 1)
	_, err = b.docker.Exec(ctx, first, []string{
		"bitcoin-cli", "-regtest",
		"-rpcuser=nodetester", "-rpcpassword=nodetester",
		"generatetoaddress", "101", addr,
	})
	if err != nil {
		return fmt.Errorf("generating blocks: %w", err)
	}

	return nil
}

// WaitForBlocks waits until at least one block is seen on the network.
func (b *BtcDevnet) WaitForBlocks(ctx context.Context) error {
	deadline := time.After(30 * time.Second)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("timeout waiting for bitcoin blocks")
		case <-ticker.C:
			output, err := b.docker.Exec(ctx, b.nodes[0], []string{
				"bitcoin-cli", "-regtest",
				"-rpcuser=nodetester", "-rpcpassword=nodetester",
				"getblockcount",
			})
			if err != nil {
				continue
			}
			count := strings.TrimSpace(output)
			if count != "0" && count != "" {
				return nil
			}
		}
	}
}
