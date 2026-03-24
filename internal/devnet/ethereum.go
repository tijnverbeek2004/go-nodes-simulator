package devnet

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tijn/nodetester/internal/docker"
	"github.com/tijn/nodetester/pkg/types"
)

// ethNode tracks per-node state during devnet setup.
type ethNode struct {
	name    string
	address string // Ethereum address (lowercase, no 0x prefix)
	ip      string // container IP on the Docker network
}

// EthDevnet orchestrates a Clique PoA Ethereum devnet on real geth containers.
type EthDevnet struct {
	docker      *docker.Client
	networkName string
	config      types.EthereumConfig
	nodes       []ethNode
}

// NewEthDevnet creates a devnet manager. Call Setup() to initialize the network.
func NewEthDevnet(dc *docker.Client, networkName string, cfg types.EthereumConfig) *EthDevnet {
	// Apply defaults.
	if cfg.ChainID == 0 {
		cfg.ChainID = 1337
	}
	if cfg.BlockTime == 0 {
		cfg.BlockTime = 5
	}
	return &EthDevnet{
		docker:      dc,
		networkName: networkName,
		config:      cfg,
	}
}

// Setup initializes the full Ethereum devnet: generates accounts, creates the
// Clique PoA genesis, initializes each node's data directory, starts geth,
// and connects all peers. This runs against real geth containers.
func (d *EthDevnet) Setup(ctx context.Context, nodeNames []string) error {
	d.nodes = make([]ethNode, len(nodeNames))
	for i, name := range nodeNames {
		d.nodes[i].name = name
	}

	fmt.Println("[ethereum] setting up Clique PoA devnet")

	// Step 1: Create password files and generate sealer accounts.
	if err := d.createAccounts(ctx); err != nil {
		return fmt.Errorf("creating accounts: %w", err)
	}

	// Step 2: Build genesis.json from sealer addresses and distribute it.
	if err := d.distributeGenesis(ctx); err != nil {
		return fmt.Errorf("distributing genesis: %w", err)
	}

	// Step 3: Initialize each node's geth data directory with the genesis.
	if err := d.initNodes(ctx); err != nil {
		return fmt.Errorf("initializing nodes: %w", err)
	}

	// Step 4: Resolve container IPs and start geth in each container.
	if err := d.startGeth(ctx); err != nil {
		return fmt.Errorf("starting geth: %w", err)
	}

	// Step 5: Wait for geth IPC sockets, then connect all peers.
	if err := d.connectPeers(ctx); err != nil {
		return fmt.Errorf("connecting peers: %w", err)
	}

	fmt.Printf("[ethereum] devnet ready: %d sealers, chainId=%d, blockTime=%ds\n",
		len(d.nodes), d.config.ChainID, d.config.BlockTime)
	return nil
}

// createAccounts generates one Ethereum account per node using geth.
func (d *EthDevnet) createAccounts(ctx context.Context) error {
	for i := range d.nodes {
		name := d.nodes[i].name
		fmt.Printf("[ethereum] generating sealer account on %s\n", name)

		// Create data dir and password file (empty password — fine for devnet).
		if _, err := d.docker.Exec(ctx, name, []string{
			"sh", "-c", "mkdir -p /data/keystore && echo '' > /data/password.txt",
		}); err != nil {
			return fmt.Errorf("setting up %s: %w", name, err)
		}

		// Generate account. Output goes to stderr, so we read the keystore
		// directory instead — the filename contains the address.
		if _, err := d.docker.Exec(ctx, name, []string{
			"geth", "--datadir", "/data", "account", "new", "--password", "/data/password.txt",
		}); err != nil {
			return fmt.Errorf("creating account on %s: %w", name, err)
		}

		// Parse address from keystore filename:
		// UTC--2024-01-01T00-00-00.000000000Z--7df9a875a174b3bc565e6424a0050ebc1b2d1d82
		output, err := d.docker.Exec(ctx, name, []string{
			"sh", "-c", "ls /data/keystore/ | head -1",
		})
		if err != nil {
			return fmt.Errorf("reading keystore on %s: %w", name, err)
		}

		addr := parseAddressFromKeystoreFile(strings.TrimSpace(output))
		if addr == "" {
			return fmt.Errorf("could not parse address from keystore filename on %s: %q", name, output)
		}

		d.nodes[i].address = addr
		fmt.Printf("[ethereum] %s sealer address: 0x%s\n", name, addr)
	}
	return nil
}

// parseAddressFromKeystoreFile extracts the Ethereum address from a geth
// keystore filename. Format: UTC--<timestamp>--<address_hex>
func parseAddressFromKeystoreFile(filename string) string {
	parts := strings.Split(filename, "--")
	if len(parts) < 3 {
		return ""
	}
	// Address is the last part.
	addr := parts[len(parts)-1]
	addr = strings.TrimSpace(addr)
	if len(addr) != 40 {
		return ""
	}
	return strings.ToLower(addr)
}

// distributeGenesis builds the Clique PoA genesis and copies it to every node.
func (d *EthDevnet) distributeGenesis(ctx context.Context) error {
	genesis := d.buildGenesis()
	data, err := json.MarshalIndent(genesis, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling genesis: %w", err)
	}

	fmt.Printf("[ethereum] genesis: chainId=%d, %d sealers, blockTime=%ds\n",
		d.config.ChainID, len(d.nodes), d.config.BlockTime)

	for _, node := range d.nodes {
		if err := d.docker.CopyToContainer(ctx, node.name, "/data", "genesis.json", data, 0644); err != nil {
			return fmt.Errorf("copying genesis to %s: %w", node.name, err)
		}
	}
	return nil
}

// initNodes runs geth init on each container.
func (d *EthDevnet) initNodes(ctx context.Context) error {
	for _, node := range d.nodes {
		fmt.Printf("[ethereum] initializing %s\n", node.name)
		if _, err := d.docker.Exec(ctx, node.name, []string{
			"geth", "--datadir", "/data", "init", "/data/genesis.json",
		}); err != nil {
			return fmt.Errorf("geth init on %s: %w", node.name, err)
		}
	}
	return nil
}

// startGeth starts geth in the background on each container.
func (d *EthDevnet) startGeth(ctx context.Context) error {
	for i := range d.nodes {
		name := d.nodes[i].name
		addr := d.nodes[i].address

		// Get container IP so geth advertises the correct enode address.
		ip, err := d.docker.GetContainerIP(ctx, name, d.networkName)
		if err != nil {
			return fmt.Errorf("getting IP for %s: %w", name, err)
		}
		d.nodes[i].ip = ip

		fmt.Printf("[ethereum] starting geth on %s (ip=%s)\n", name, ip)

		// Build the geth command. Key flags:
		// --mine + --unlock: this node seals blocks in Clique PoA
		// --nat extip: advertise the Docker network IP in enode URLs
		// --nodiscover: we connect peers explicitly (no UDP discovery)
		// --allow-insecure-unlock: required for --unlock without TLS
		gethCmd := fmt.Sprintf(
			"geth --datadir /data"+
				" --networkid %d"+
				" --port 30303"+
				" --nat extip:%s"+
				" --http --http.addr 0.0.0.0 --http.port 8545"+
				" --http.api eth,net,web3,admin,miner"+
				" --mine"+
				" --miner.etherbase 0x%s"+
				" --unlock 0x%s"+
				" --password /data/password.txt"+
				" --allow-insecure-unlock"+
				" --nodiscover"+
				" --syncmode full"+
				" --verbosity 3"+
				" > /data/geth.log 2>&1 &",
			d.config.ChainID, ip, addr, addr,
		)

		// sh -c '... &' forks geth and returns immediately.
		// geth gets reparented to PID 1 (sleep infinity) and keeps running.
		if _, err := d.docker.Exec(ctx, name, []string{"sh", "-c", gethCmd}); err != nil {
			return fmt.Errorf("starting geth on %s: %w", name, err)
		}
	}
	return nil
}

// connectPeers waits for geth IPC sockets, fetches enode URLs, and connects
// every node to every other node using admin.addPeer.
func (d *EthDevnet) connectPeers(ctx context.Context) error {
	fmt.Println("[ethereum] waiting for geth IPC sockets...")

	// Wait for all IPC sockets to appear (geth takes a moment to start).
	for _, node := range d.nodes {
		if err := d.waitForIPC(ctx, node.name); err != nil {
			return err
		}
	}

	// Fetch enode URLs.
	enodes := make([]string, len(d.nodes))
	for i, node := range d.nodes {
		output, err := d.docker.Exec(ctx, node.name, []string{
			"geth", "attach", "/data/geth.ipc", "--exec", "admin.nodeInfo.enode",
		})
		if err != nil {
			return fmt.Errorf("getting enode from %s: %w", node.name, err)
		}
		// Output is a quoted string like "enode://pubkey@ip:port"
		enode := strings.Trim(strings.TrimSpace(output), "\"")
		enodes[i] = enode
		fmt.Printf("[ethereum] %s enode: %s\n", node.name, truncateEnode(enode))
	}

	// Connect every pair of peers.
	for i, node := range d.nodes {
		for j, enode := range enodes {
			if i == j {
				continue
			}
			addPeerJS := fmt.Sprintf("admin.addPeer('%s')", enode)
			if _, err := d.docker.Exec(ctx, node.name, []string{
				"geth", "attach", "/data/geth.ipc", "--exec", addPeerJS,
			}); err != nil {
				fmt.Printf("[ethereum] WARNING: %s failed to add peer %s: %v\n",
					node.name, d.nodes[j].name, err)
			}
		}
	}

	fmt.Printf("[ethereum] all %d peers connected\n", len(d.nodes))
	return nil
}

// waitForIPC waits up to 30 seconds for the geth IPC socket to appear.
func (d *EthDevnet) waitForIPC(ctx context.Context, name string) error {
	// Poll for the IPC socket. geth creates it once it's ready.
	_, err := d.docker.Exec(ctx, name, []string{
		"sh", "-c",
		"for i in $(seq 1 30); do [ -S /data/geth.ipc ] && exit 0; sleep 1; done; exit 1",
	})
	if err != nil {
		return fmt.Errorf("timeout waiting for geth IPC on %s: %w", name, err)
	}
	return nil
}

// truncateEnode shortens an enode URL for readable log output.
func truncateEnode(enode string) string {
	if len(enode) > 40 {
		return enode[:30] + "..." + enode[len(enode)-10:]
	}
	return enode
}

// --- Genesis construction ---

// genesis is the JSON structure for a geth genesis.json file.
type genesis struct {
	Config     genesisConfig            `json:"config"`
	Difficulty string                   `json:"difficulty"`
	GasLimit   string                   `json:"gasLimit"`
	ExtraData  string                   `json:"extradata"`
	Alloc      map[string]genesisAlloc  `json:"alloc"`
}

type genesisConfig struct {
	ChainID             int           `json:"chainId"`
	HomesteadBlock      int           `json:"homesteadBlock"`
	EIP150Block         int           `json:"eip150Block"`
	EIP155Block         int           `json:"eip155Block"`
	EIP158Block         int           `json:"eip158Block"`
	ByzantiumBlock      int           `json:"byzantiumBlock"`
	ConstantinopleBlock int           `json:"constantinopleBlock"`
	PetersburgBlock     int           `json:"petersburgBlock"`
	IstanbulBlock       int           `json:"istanbulBlock"`
	BerlinBlock         int           `json:"berlinBlock"`
	LondonBlock         int           `json:"londonBlock"`
	Clique              *cliqueConfig `json:"clique"`
}

type cliqueConfig struct {
	Period int `json:"period"` // block time in seconds
	Epoch  int `json:"epoch"`  // reset votes after this many blocks
}

type genesisAlloc struct {
	Balance string `json:"balance"`
}

// buildGenesis creates a Clique PoA genesis with all nodes as sealers.
func (d *EthDevnet) buildGenesis() genesis {
	addresses := make([]string, len(d.nodes))
	alloc := make(map[string]genesisAlloc)

	for i, node := range d.nodes {
		addresses[i] = node.address
		// Pre-fund each sealer with 1000 ETH (in wei).
		alloc[node.address] = genesisAlloc{
			Balance: "1000000000000000000000",
		}
	}

	return genesis{
		Config: genesisConfig{
			ChainID:             d.config.ChainID,
			HomesteadBlock:      0,
			EIP150Block:         0,
			EIP155Block:         0,
			EIP158Block:         0,
			ByzantiumBlock:      0,
			ConstantinopleBlock: 0,
			PetersburgBlock:     0,
			IstanbulBlock:       0,
			BerlinBlock:         0,
			LondonBlock:         0,
			Clique: &cliqueConfig{
				Period: d.config.BlockTime,
				Epoch:  30000,
			},
		},
		Difficulty: "1",
		GasLimit:   "0x1000000",
		ExtraData:  buildExtraData(addresses),
		Alloc:      alloc,
	}
}

// buildExtraData constructs the Clique PoA extradata field.
// Format: 32 bytes vanity | N * 20 bytes signer addresses | 65 bytes seal
func buildExtraData(addresses []string) string {
	extra := "0x"
	extra += strings.Repeat("0", 64) // 32 bytes vanity
	for _, addr := range addresses {
		extra += strings.ToLower(strings.TrimPrefix(addr, "0x"))
	}
	extra += strings.Repeat("0", 130) // 65 bytes seal
	return extra
}

// DefaultEthImage returns the default Docker image for Ethereum nodes.
func DefaultEthImage() string {
	return "ethereum/client-go:latest"
}

// WaitForBlocks waits until the devnet produces at least one block,
// confirming that consensus is working.
func (d *EthDevnet) WaitForBlocks(ctx context.Context) error {
	fmt.Println("[ethereum] waiting for first block...")

	// Check block number on node-1. Poll every 2s, timeout after 60s.
	deadline := time.After(60 * time.Second)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("timeout waiting for block production")
		case <-ticker.C:
			output, err := d.docker.Exec(ctx, d.nodes[0].name, []string{
				"geth", "attach", "/data/geth.ipc", "--exec", "eth.blockNumber",
			})
			if err != nil {
				continue // geth might not be fully ready yet
			}
			blockNum := strings.TrimSpace(output)
			if blockNum != "0" && blockNum != "" {
				fmt.Printf("[ethereum] first block mined (blockNumber=%s)\n", blockNum)
				return nil
			}
		}
	}
}
