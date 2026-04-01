package types

import "time"

// Scenario represents a full test scenario parsed from YAML.
type Scenario struct {
	Nodes      NodeSpec    `yaml:"nodes"`
	Events     []Event     `yaml:"events"`
	Assertions []Assertion `yaml:"assertions,omitempty"`
}

// NodeSpec defines what containers to spin up.
type NodeSpec struct {
	Image    string            `yaml:"image"`
	Count    int               `yaml:"count"`
	Network  string            `yaml:"network,omitempty"` // auto-generated if empty
	Env      map[string]string `yaml:"env,omitempty"`
	Command  []string          `yaml:"command,omitempty"`
	Preset   string            `yaml:"preset,omitempty"`   // "ethereum", "bitcoin", "cosmos", "solana", or empty
	Ethereum *EthereumConfig   `yaml:"ethereum,omitempty"` // settings for preset: ethereum
	Bitcoin  *BitcoinConfig    `yaml:"bitcoin,omitempty"`  // settings for preset: bitcoin
	Cosmos   *CosmosConfig     `yaml:"cosmos,omitempty"`   // settings for preset: cosmos
	Solana   *SolanaConfig     `yaml:"solana,omitempty"`   // settings for preset: solana
	Binary   *CustomBinary     `yaml:"binary,omitempty"`   // inject a custom P2P binary into containers
}

// EthereumConfig holds settings for a Clique PoA devnet.
type EthereumConfig struct {
	ChainID   int `yaml:"chain_id"`   // network/chain ID (default: 1337)
	BlockTime int `yaml:"block_time"` // seconds between PoA blocks (default: 5)
}

// BitcoinConfig holds settings for a Bitcoin regtest network.
type BitcoinConfig struct {
	BlockTime int `yaml:"block_time"` // seconds between generated blocks (default: 10)
}

// CosmosConfig holds settings for a Cosmos SDK localnet.
type CosmosConfig struct {
	ChainID   string `yaml:"chain_id"`   // chain ID (default: "nodetester-1")
	BlockTime string `yaml:"block_time"` // tendermint block time (default: "1s")
}

// SolanaConfig holds settings for a Solana test validator cluster.
type SolanaConfig struct {
	SlotsPerEpoch int `yaml:"slots_per_epoch"` // slots per epoch (default: 50)
}

// CustomBinary specifies a local binary to copy into each container and run.
// The binary must be compiled for linux/amd64 (or the container's platform).
type CustomBinary struct {
	Path string   `yaml:"path"` // host path to the binary (relative to CWD)
	Args []string `yaml:"args"` // arguments passed to the binary inside the container
}

// Event is a single chaos action scheduled at a point in time.
type Event struct {
	At     Duration          `yaml:"at"`              // e.g. "10s", "1m30s"
	Action string            `yaml:"action"`          // stop, restart, latency, partition, etc.
	Target string            `yaml:"target"`          // node-2, node-*, random(2)
	Params map[string]string `yaml:"params,omitempty"`
	Every  Duration          `yaml:"every,omitempty"` // repeat interval (e.g. "10s")
	Count  int               `yaml:"count,omitempty"` // max repeat count (requires every)
	Until  Duration          `yaml:"until,omitempty"` // repeat until this time from scenario start (requires every)
	If     string            `yaml:"if,omitempty"`    // condition: "node-1.state == exited"
}

// Assertion defines an expected condition checked at a point in time.
type Assertion struct {
	At       Duration `yaml:"at"`                 // when to check
	Type     string   `yaml:"type"`               // "state" or "exec"
	Target   string   `yaml:"target"`             // node name or glob
	Expect   string   `yaml:"expect,omitempty"`   // state: "running"/"exited"; exec: "success"/"failure" (default: contextual)
	Command  string   `yaml:"command,omitempty"`  // exec type: command to run
	Contains string   `yaml:"contains,omitempty"` // exec type: check output contains this string
}

// AssertionResult records the outcome of an assertion check.
type AssertionResult struct {
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	Target    string    `json:"target"`
	Success   bool      `json:"success"`
	Message   string    `json:"message"`
}

// Duration wraps time.Duration to support YAML unmarshaling from strings like "10s".
type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var s string
	if err := unmarshal(&s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	d.Duration = parsed
	return nil
}

// NodeStatus captures the observed state of a single container.
type NodeStatus struct {
	Name         string    `json:"name"`
	ContainerID  string    `json:"container_id"`
	State        string    `json:"state"` // running, exited, paused, etc.
	RestartCount int       `json:"restart_count"`
	LastChecked  time.Time `json:"last_checked"`
}

// ContainerStats holds a point-in-time resource snapshot for a container.
type ContainerStats struct {
	CPUPercent float64 `json:"cpu_percent"`
	MemUsage   uint64  `json:"mem_usage_bytes"`
	MemLimit   uint64  `json:"mem_limit_bytes"`
	NetRx      uint64  `json:"net_rx_bytes"`
	NetTx      uint64  `json:"net_tx_bytes"`
}

// StatsSnapshot holds stats for all nodes at a point in time.
type StatsSnapshot struct {
	Timestamp time.Time                `json:"timestamp"`
	Nodes     map[string]ContainerStats `json:"nodes"`
}

// EventRecord logs a chaos event that was executed.
type EventRecord struct {
	Timestamp time.Time `json:"timestamp"`
	Action    string    `json:"action"`
	Target    string    `json:"target"`
	Success   bool      `json:"success"`
	Error     string    `json:"error,omitempty"`
}
