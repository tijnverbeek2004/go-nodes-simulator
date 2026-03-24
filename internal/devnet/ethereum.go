package devnet

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tijnverbeek2004/nodetester/internal/docker"
	"github.com/tijnverbeek2004/nodetester/pkg/types"
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
	status      func(string) // optional callback for progress updates
}

// NewEthDevnet creates a devnet manager. Call Setup() to initialize the network.
func NewEthDevnet(dc *docker.Client, networkName string, cfg types.EthereumConfig) *EthDevnet {
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

func (d *EthDevnet) emit(msg string) {
	if d.status != nil {
		d.status(msg)
	}
}

// Setup initializes the full Ethereum devnet: generates accounts, creates the
// Clique PoA genesis, initializes each node's data directory, starts geth,
// and connects all peers. statusFn is called with progress updates (may be nil).
func (d *EthDevnet) Setup(ctx context.Context, nodeNames []string, statusFn func(string)) error {
	d.status = statusFn
	d.nodes = make([]ethNode, len(nodeNames))
	for i, name := range nodeNames {
		d.nodes[i].name = name
	}

	d.emit("generating sealer accounts...")
	if err := d.createAccounts(ctx); err != nil {
		return fmt.Errorf("creating accounts: %w", err)
	}

	d.emit("distributing genesis...")
	if err := d.distributeGenesis(ctx); err != nil {
		return fmt.Errorf("distributing genesis: %w", err)
	}

	d.emit("initializing geth nodes...")
	if err := d.initNodes(ctx); err != nil {
		return fmt.Errorf("initializing nodes: %w", err)
	}

	d.emit("starting geth...")
	if err := d.startGeth(ctx); err != nil {
		return fmt.Errorf("starting geth: %w", err)
	}

	d.emit("connecting peers...")
	if err := d.connectPeers(ctx); err != nil {
		return fmt.Errorf("connecting peers: %w", err)
	}

	return nil
}

// createAccounts generates one Ethereum account per node using geth.
func (d *EthDevnet) createAccounts(ctx context.Context) error {
	for i := range d.nodes {
		name := d.nodes[i].name

		if _, err := d.docker.Exec(ctx, name, []string{
			"sh", "-c", "mkdir -p /data/keystore && echo '' > /data/password.txt",
		}); err != nil {
			return fmt.Errorf("setting up %s: %w", name, err)
		}

		if _, err := d.docker.Exec(ctx, name, []string{
			"geth", "--datadir", "/data", "account", "new", "--password", "/data/password.txt",
		}); err != nil {
			return fmt.Errorf("creating account on %s: %w", name, err)
		}

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

		ip, err := d.docker.GetContainerIP(ctx, name, d.networkName)
		if err != nil {
			return fmt.Errorf("getting IP for %s: %w", name, err)
		}
		d.nodes[i].ip = ip

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

		if _, err := d.docker.Exec(ctx, name, []string{"sh", "-c", gethCmd}); err != nil {
			return fmt.Errorf("starting geth on %s: %w", name, err)
		}
	}
	return nil
}

// connectPeers waits for geth IPC sockets, fetches enode URLs, and connects
// every node to every other node using admin.addPeer.
func (d *EthDevnet) connectPeers(ctx context.Context) error {
	for _, node := range d.nodes {
		if err := d.waitForIPC(ctx, node.name); err != nil {
			return err
		}
	}

	enodes := make([]string, len(d.nodes))
	for i, node := range d.nodes {
		output, err := d.docker.Exec(ctx, node.name, []string{
			"sh", "-c", "geth attach --exec 'admin.nodeInfo.enode' /data/geth.ipc",
		})
		if err != nil {
			return fmt.Errorf("getting enode from %s: %w", node.name, err)
		}
		enodes[i] = strings.Trim(strings.TrimSpace(output), "\"")
	}

	for i, node := range d.nodes {
		for j, enode := range enodes {
			if i == j {
				continue
			}
			addPeerCmd := fmt.Sprintf("geth attach --exec \"admin.addPeer('%s')\" /data/geth.ipc", enode)
			if _, err := d.docker.Exec(ctx, node.name, []string{
				"sh", "-c", addPeerCmd,
			}); err != nil {
				// Non-fatal: peer connection can be retried by geth
				continue
			}
		}
	}

	return nil
}

// waitForIPC waits up to 30 seconds for the geth IPC socket to appear.
func (d *EthDevnet) waitForIPC(ctx context.Context, name string) error {
	_, err := d.docker.Exec(ctx, name, []string{
		"sh", "-c",
		"for i in $(seq 1 30); do [ -S /data/geth.ipc ] && exit 0; sleep 1; done; exit 1",
	})
	if err != nil {
		return fmt.Errorf("timeout waiting for geth IPC on %s: %w", name, err)
	}
	return nil
}

// --- Genesis construction ---

type genesis struct {
	Config     genesisConfig           `json:"config"`
	Difficulty string                  `json:"difficulty"`
	GasLimit   string                  `json:"gasLimit"`
	ExtraData  string                  `json:"extradata"`
	Alloc      map[string]genesisAlloc `json:"alloc"`
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
	Period int `json:"period"`
	Epoch  int `json:"epoch"`
}

type genesisAlloc struct {
	Balance string `json:"balance"`
}

func (d *EthDevnet) buildGenesis() genesis {
	addresses := make([]string, len(d.nodes))
	alloc := make(map[string]genesisAlloc)

	for i, node := range d.nodes {
		addresses[i] = node.address
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

func buildExtraData(addresses []string) string {
	extra := "0x"
	extra += strings.Repeat("0", 64)
	for _, addr := range addresses {
		extra += strings.ToLower(strings.TrimPrefix(addr, "0x"))
	}
	extra += strings.Repeat("0", 130)
	return extra
}

// DefaultEthImage returns the default Docker image for Ethereum nodes.
// v1.13.15 is the last release with Clique PoA support — v1.14+ is PoS only.
func DefaultEthImage() string {
	return "ethereum/client-go:v1.13.15"
}

// WaitForBlocks waits until the devnet produces at least one block.
func (d *EthDevnet) WaitForBlocks(ctx context.Context) error {
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
				"sh", "-c", "geth attach --exec 'eth.blockNumber' /data/geth.ipc",
			})
			if err != nil {
				continue
			}
			blockNum := strings.TrimSpace(output)
			if blockNum != "0" && blockNum != "" {
				return nil
			}
		}
	}
}
