# 3-Machine Travel Agency Demo

Demonstrates tailbus agent discovery and messaging across 3 machines using the
ServiceManifest system.

## Topology

| Machine | Role | Agents |
|---------|------|--------|
| A | Coord + daemon | `concierge` (orchestrator) |
| B | Daemon only | `flights`, `hotels` (booking) |
| C | Daemon only | `weather`, `currency` (data) |

## Prerequisites

Install tailbus on all 3 machines:

```sh
curl -sSL https://raw.githubusercontent.com/alexanderfrey/tailbus/main/install.sh | sh
```

## Setup

### 1. Configure IPs

On each machine, copy the relevant config and replace the placeholders:

```sh
# Machine A (coord + daemon) — replace with your actual IPs
COORD_IP="10.0.0.1"   # Machine A's IP
MY_IP="10.0.0.1"

sed "s/__COORD_IP__/$COORD_IP/; s/__MY_IP__/$MY_IP/" machine-a.toml > /tmp/tailbusd.toml
```

```sh
# Machine B
COORD_IP="10.0.0.1"   # Machine A's IP
MY_IP="10.0.0.2"      # Machine B's IP

sed "s/__COORD_IP__/$COORD_IP/; s/__MY_IP__/$MY_IP/" machine-b.toml > /tmp/tailbusd.toml
```

```sh
# Machine C
COORD_IP="10.0.0.1"   # Machine A's IP
MY_IP="10.0.0.3"      # Machine C's IP

sed "s/__COORD_IP__/$COORD_IP/; s/__MY_IP__/$MY_IP/" machine-c.toml > /tmp/tailbusd.toml
```

### 2. Start the coord server (Machine A only)

```sh
tailbus-coord -config coord.toml
```

### 3. Start daemons (all machines)

```sh
tailbusd -config /tmp/tailbusd.toml
```

### 4. Register agents

```sh
# Machine A
./register-agents.sh machine-a

# Machine B
./register-agents.sh machine-b

# Machine C
./register-agents.sh machine-c
```

### 5. Explore

From any machine:

```sh
# List all agents across the mesh
tailbus list

# Filter by tag
tailbus list booking
tailbus list data

# Introspect a remote agent
tailbus introspect flights
tailbus introspect weather

# Open a cross-machine session
tailbus open concierge flights "Search NYC to London, Dec 20-27"
```

Or run the interactive walkthrough:

```sh
./demo.sh
```

## Local single-machine test

To try everything on one machine, use the dev configs in `examples/dev/` or
start 3 daemons with different socket paths and ports:

```sh
# Terminal 1: coord
tailbus-coord -config coord.toml

# Terminal 2: daemon A
tailbusd -config machine-a.toml   # after sed-replacing __COORD_IP__ and __MY_IP__ with 127.0.0.1

# Terminal 3: daemon B (edit ports to avoid conflicts)
# Override listen_addr and socket_path to avoid collisions with daemon A
```
