package types

import "time"

// Scenario represents a full test scenario parsed from YAML.
type Scenario struct {
	Nodes  NodeSpec `yaml:"nodes"`
	Events []Event  `yaml:"events"`
}

// NodeSpec defines what containers to spin up.
type NodeSpec struct {
	Image    string            `yaml:"image"`
	Count    int               `yaml:"count"`
	Network  string            `yaml:"network,omitempty"` // auto-generated if empty
	Env      map[string]string `yaml:"env,omitempty"`
	Command  []string          `yaml:"command,omitempty"`
	Preset   string            `yaml:"preset,omitempty"`   // "ethereum" for geth devnet, empty for generic
	Ethereum *EthereumConfig   `yaml:"ethereum,omitempty"` // required when preset is "ethereum"
	Binary   *CustomBinary     `yaml:"binary,omitempty"`   // inject a custom P2P binary into containers
}

// EthereumConfig holds settings for a Clique PoA devnet.
type EthereumConfig struct {
	ChainID   int `yaml:"chain_id"`   // network/chain ID (default: 1337)
	BlockTime int `yaml:"block_time"` // seconds between PoA blocks (default: 5)
}

// CustomBinary specifies a local binary to copy into each container and run.
// The binary must be compiled for linux/amd64 (or the container's platform).
type CustomBinary struct {
	Path string   `yaml:"path"` // host path to the binary (relative to CWD)
	Args []string `yaml:"args"` // arguments passed to the binary inside the container
}

// Event is a single chaos action scheduled at a point in time.
type Event struct {
	At     Duration          `yaml:"at"`     // e.g. "10s", "1m30s"
	Action string            `yaml:"action"` // stop, restart, latency, partition
	Target string            `yaml:"target"` // node-2, node-*, group syntax
	Params map[string]string `yaml:"params,omitempty"`
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

// EventRecord logs a chaos event that was executed.
type EventRecord struct {
	Timestamp time.Time `json:"timestamp"`
	Action    string    `json:"action"`
	Target    string    `json:"target"`
	Success   bool      `json:"success"`
	Error     string    `json:"error,omitempty"`
}
