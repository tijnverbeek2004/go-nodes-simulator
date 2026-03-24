# nodetester

A chaos-testing CLI tool for distributed node systems. Spin up real Docker containers, execute failure scenarios, and collect results — all driven by simple YAML configs.

Built for testing blockchain networks, consensus protocols, and P2P applications under adverse conditions.

## Features

- **Container orchestration** — Spin up 1–50 Docker containers with a single command
- **Chaos actions** — Stop, restart, inject latency, create network partitions, and heal
- **Ethereum devnet support** — Full Clique PoA devnet setup with automatic peer discovery
- **Custom binary injection** — Test your own P2P node implementations inside containers
- **Flexible targeting** — Target specific nodes (`node-2`), multiple nodes (`node-1,node-3`), or all nodes (`node-*`)
- **JSON reporting** — Structured output of node states and event outcomes

## Quick Start

### Prerequisites

- Docker running and accessible
- Go 1.26+ (to build from source)

### Install

```bash
go install github.com/tijn/nodetester@latest
```

Or build from source:

```bash
git clone https://github.com/tijnverbeek2004/go-nodes-simulator.git
cd go-nodes-simulator
go build -o nodetester .
```

### Run a scenario

```bash
nodetester run examples/basic-stop.yaml
```

## Usage

```
nodetester run <scenario.yaml> [-r report.json]   # Execute a chaos scenario
nodetester status                                   # Show running nodetester containers
nodetester cleanup                                  # Remove all nodetester containers and networks
```

## Scenario Format

Scenarios are YAML files with two sections: `nodes` (what to spin up) and `events` (what to do to them).

### Basic example

```yaml
nodes:
  image: alpine
  count: 3

events:
  - at: 5s
    action: stop
    target: node-2

  - at: 10s
    action: restart
    target: node-2
```

### Ethereum devnet example

```yaml
nodes:
  preset: ethereum
  count: 4
  ethereum:
    chain_id: 1337
    block_time: 5

events:
  - at: 30s
    action: stop
    target: node-2

  - at: 45s
    action: stop
    target: node-3

  - at: 60s
    action: restart
    target: node-2

  - at: 75s
    action: latency
    target: node-1,node-4
    params:
      ms: "500"
```

## Chaos Actions

| Action | Description | Params |
|---|---|---|
| `stop` | Stop a running container | — |
| `restart` | Restart a stopped container | — |
| `latency` | Inject network delay via `tc netem` | `ms` (milliseconds) |
| `partition` | Bidirectional network partition via `iptables` | `from` (target group) |
| `heal` | Remove all partition rules, restore connectivity | — |

## Custom Binary Injection

Test your own node implementation by injecting a Linux/amd64 binary:

```yaml
nodes:
  image: alpine
  count: 3
  binary:
    path: ./build/my-p2p-node
    args: ["--listen", "0.0.0.0:9000"]

events:
  - at: 10s
    action: stop
    target: node-1
```

The binary is copied to `/usr/local/bin/` inside each container and started automatically.

## Report Output

After execution, a JSON report is written with node states and event outcomes:

```json
{
  "nodes": [
    {
      "name": "node-1",
      "container_id": "abc123",
      "state": "running",
      "restarts": 0
    }
  ],
  "events": [
    {
      "timestamp": "2025-03-20T21:30:05Z",
      "action": "stop",
      "target": "node-2",
      "success": true
    }
  ]
}
```

## Project Structure

```
├── cmd/              # CLI commands (run, status, cleanup)
├── internal/
│   ├── chaos/        # Chaos action executors
│   ├── devnet/       # Ethereum devnet orchestration
│   ├── docker/       # Docker client wrapper
│   ├── metrics/      # Event recording & reporting
│   └── scenario/     # YAML config parser
├── pkg/types/        # Shared data types
└── examples/         # Example scenario files
```

## License

MIT
