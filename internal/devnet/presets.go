package devnet

import (
	"context"
	"fmt"

	"github.com/tijnverbeek2004/nodetester/internal/docker"
	"github.com/tijnverbeek2004/nodetester/pkg/types"
)

// DefaultImageForPreset returns the default Docker image for a preset.
func DefaultImageForPreset(preset string) string {
	switch preset {
	case "ethereum":
		return DefaultEthImage()
	case "bitcoin":
		return DefaultBitcoinImage
	case "cosmos":
		return DefaultCosmosImage
	case "solana":
		return DefaultSolanaImage
	default:
		return ""
	}
}

// SetupPreset runs the preset-specific setup after containers are created.
func SetupPreset(ctx context.Context, sc *types.Scenario, dc *docker.Client, networkName string, nodeNames []string, statusFn func(string)) error {
	switch sc.Nodes.Preset {
	case "ethereum":
		eth := NewEthDevnet(dc, networkName, *sc.Nodes.Ethereum)
		return eth.Setup(ctx, nodeNames, statusFn)
	case "bitcoin":
		btc := NewBtcDevnet(dc, networkName, *sc.Nodes.Bitcoin)
		return btc.Setup(ctx, nodeNames, statusFn)
	case "cosmos":
		cos := NewCosmosDevnet(dc, networkName, *sc.Nodes.Cosmos)
		return cos.Setup(ctx, nodeNames, statusFn)
	case "solana":
		sol := NewSolanaDevnet(dc, networkName, *sc.Nodes.Solana)
		return sol.Setup(ctx, nodeNames, statusFn)
	default:
		return fmt.Errorf("unknown preset %q", sc.Nodes.Preset)
	}
}

// WaitForBlocks waits for the preset-specific chain to produce blocks.
func WaitForBlocks(ctx context.Context, preset string, dc *docker.Client, networkName string, nodeNames []string, sc *types.Scenario) error {
	switch preset {
	case "ethereum":
		eth := NewEthDevnet(dc, networkName, *sc.Nodes.Ethereum)
		eth.nodes = make([]ethNode, len(nodeNames))
		for i, name := range nodeNames {
			eth.nodes[i].name = name
		}
		return eth.WaitForBlocks(ctx)
	case "bitcoin":
		btc := NewBtcDevnet(dc, networkName, *sc.Nodes.Bitcoin)
		btc.nodes = nodeNames
		return btc.WaitForBlocks(ctx)
	case "cosmos":
		cos := NewCosmosDevnet(dc, networkName, *sc.Nodes.Cosmos)
		cos.nodes = nodeNames
		return cos.WaitForBlocks(ctx)
	case "solana":
		sol := NewSolanaDevnet(dc, networkName, *sc.Nodes.Solana)
		sol.nodes = nodeNames
		return sol.WaitForBlocks(ctx)
	default:
		return fmt.Errorf("unknown preset %q", preset)
	}
}
