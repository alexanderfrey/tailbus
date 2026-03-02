# Tailbus

Agent communication mesh. Tailscale-style topology: a central coordination server for discovery, with a peer-to-peer gRPC data plane between node daemons.

Think of it as **Slack for autonomous agents** — agents register handles, open sessions, exchange messages, and resolve conversations, all routed through a decentralized mesh.

```
                       +-----------------+
                       |  tailbus-coord  |
                       |  (discovery)    |
                       +--------+--------+
                          peer map
                       /    updates     \
              +--------+--------+  +--------+--------+
              |    tailbusd     |  |    tailbusd     |
              |    (node-1)     |  |    (node-2)     |
              |  P2P gRPC <----|--|----> P2P gRPC   |
              +--------+--------+  +--------+--------+
                 Unix socket          Unix socket
                /     \                /     \
           agent-a   agent-b     agent-c   agent-d
```

## Features

- **Handle-based addressing** — agents register names like `marketing`, `sales`, `planner` and message each other without knowing which node they're on
- **Session lifecycle** — structured conversations with open / message / resolve states
- **P2P data plane** — messages flow directly between daemons via bidirectional gRPC streams, not through the coord server
- **Distributed message tracing** — every session gets a `trace_id`; spans are recorded at each hop (created, routed, sent, received, delivered)
- **Prometheus metrics** — counter and histogram metrics exported at `/metrics` for external monitoring
- **Real-time TUI dashboard** — terminal dashboard showing handles, peers, sessions, and live activity
- **mTLS-ready** — identity package generates keypairs and certs (MVP runs insecure gRPC)

## Prerequisites

- **Go 1.25+** with CGo enabled (required for SQLite)
- **protoc** with `protoc-gen-go` and `protoc-gen-go-grpc` (only needed if modifying `.proto` files)

## Build

```bash
make build
```

This produces three binaries in `bin/`:

| Binary | Description |
|--------|-------------|
| `bin/tailbus-coord` | Coordination server (discovery + peer map) |
| `bin/tailbusd` | Node daemon (local agent server + P2P transport) |
| `bin/tailbus` | CLI tool for interacting with a local daemon |

Other Makefile targets:

```bash
make proto      # Regenerate protobuf Go code
make test       # Run unit tests
make test-all   # Run all tests including integration
make clean      # Remove binaries and generated code
```

## Quick Start

### 1. Start the coordination server

```bash
./bin/tailbus-coord -listen :8443 -data-dir /tmp/tailbus-coord
```

Or with a config file:

```bash
./bin/tailbus-coord -config examples/dev/coord.toml
```

### 2. Start two node daemons

```bash
# Terminal 2 — node-1
./bin/tailbusd -config examples/dev/daemon1.toml

# Terminal 3 — node-2
./bin/tailbusd -config examples/dev/daemon2.toml
```

Or using flags directly:

```bash
./bin/tailbusd \
  -node-id node-1 \
  -coord 127.0.0.1:8443 \
  -advertise 127.0.0.1:9443 \
  -listen :9443 \
  -socket /tmp/tailbusd-1.sock
```

### 3. Register agents and exchange messages

```bash
# Register handles on each node
./bin/tailbus -socket /tmp/tailbusd-1.sock register marketing
./bin/tailbus -socket /tmp/tailbusd-2.sock register sales

# Subscribe to incoming messages (blocking — run in separate terminals)
./bin/tailbus -socket /tmp/tailbusd-1.sock subscribe marketing
./bin/tailbus -socket /tmp/tailbusd-2.sock subscribe sales

# Open a session from marketing to sales
./bin/tailbus -socket /tmp/tailbusd-1.sock open marketing sales "Need Q4 numbers"
# Output: Session: <session-id>  Message: <message-id>

# Reply from sales
./bin/tailbus -socket /tmp/tailbusd-2.sock send <session-id> sales "Q4 revenue: $1.2M"

# Resolve the session
./bin/tailbus -socket /tmp/tailbusd-1.sock resolve <session-id> marketing "Thanks!"
```

### 4. Observe

```bash
# Launch the TUI dashboard
./bin/tailbus -socket /tmp/tailbusd-1.sock dashboard

# List sessions for a handle
./bin/tailbus -socket /tmp/tailbusd-1.sock sessions marketing

# View a distributed trace
./bin/tailbus -socket /tmp/tailbusd-1.sock trace <trace-id>

# Scrape Prometheus metrics
curl http://localhost:9090/metrics
```

## Configuration

Both the coord server and daemon accept TOML config files via `-config`. Example files are in `examples/dev/`.

### Coordination server (`tailbus-coord`)

```toml
listen_addr = ":8443"
data_dir = "/tmp/tailbus-coord"
```

| Field | Default | Description |
|-------|---------|-------------|
| `listen_addr` | `:8443` | gRPC listen address |
| `data_dir` | `.` | Directory for SQLite database |

### Node daemon (`tailbusd`)

```toml
node_id = "node-1"
coord_addr = "127.0.0.1:8443"
advertise_addr = "127.0.0.1:9443"
listen_addr = ":9443"
socket_path = "/tmp/tailbusd-1.sock"
key_file = "/tmp/tailbusd-node1.key"
metrics_addr = ":9090"
```

| Field | Default | Description |
|-------|---------|-------------|
| `node_id` | hostname | Unique identifier for this node |
| `coord_addr` | `127.0.0.1:8443` | Coordination server address |
| `advertise_addr` | (required) | Address other daemons use to reach this node |
| `listen_addr` | `:9443` | P2P gRPC listen address |
| `socket_path` | `/tmp/tailbusd.sock` | Unix socket for local agent connections |
| `key_file` | `/tmp/tailbusd-{nodeID}.key` | Node keypair file (auto-generated if missing) |
| `metrics_addr` | `:9090` | Prometheus HTTP endpoint (empty string disables) |

All config fields can be overridden with command-line flags. Run any binary with `-help` to see available flags.

## CLI Reference

```
tailbus [flags] <command> [args]
```

**Global flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `-socket` | `/tmp/tailbusd.sock` | Path to local daemon Unix socket |

**Commands:**

| Command | Usage | Description |
|---------|-------|-------------|
| `register` | `register <handle>` | Register an agent handle on the local node |
| `open` | `open <from> <to> <message>` | Open a new session with an initial message |
| `send` | `send <session-id> <from> <message>` | Send a message within an existing session |
| `subscribe` | `subscribe <handle>` | Stream incoming messages (blocks until Ctrl-C) |
| `resolve` | `resolve <session-id> <from> [message]` | Resolve (close) a session with an optional final message |
| `sessions` | `sessions <handle>` | List sessions involving a handle |
| `dashboard` | `dashboard` | Launch interactive TUI dashboard |
| `trace` | `trace <trace-id>` | Display distributed trace spans for a trace ID |

## Distributed Tracing

Every session is assigned a `trace_id` (auto-generated UUID, or agent-provided for external correlation). The trace ID propagates on every envelope in that session. Spans are recorded at each hop:

| Action | Where | Description |
|--------|-------|-------------|
| `MESSAGE_CREATED` | AgentServer | Message created (open, send, or resolve) |
| `ROUTED_LOCAL` | MessageRouter | Delivered to a local subscriber |
| `ROUTED_REMOTE` | MessageRouter | Forwarded to a remote peer |
| `SENT_TO_TRANSPORT` | GRPCTransport | Successfully sent over P2P stream |
| `RECEIVED_FROM_TRANSPORT` | Daemon | Received from P2P stream |
| `DELIVERED_TO_SUBSCRIBER` | AgentServer | Delivered to agent subscriber channel |

Spans are stored in an in-memory ring buffer (10,000 spans per node). Query via CLI:

```bash
$ ./bin/tailbus trace 1ee8ae5a-61fe-495f-a9e0-ae4395ef2f40

Trace 1ee8ae5a-61fe-495f-a9e0-ae4395ef2f40 (6 spans):

  15:51:35.345  TRACE_ACTION_MESSAGE_CREATED      msg:42c03d06  node:node-1
  15:51:35.346  TRACE_ACTION_SENT_TO_TRANSPORT     msg:42c03d06  node:node-1
  15:51:35.346  TRACE_ACTION_ROUTED_REMOTE         msg:42c03d06  node:node-1
  15:51:35.349  TRACE_ACTION_MESSAGE_CREATED       msg:9ceb8279  node:node-1
  15:51:35.349  TRACE_ACTION_SENT_TO_TRANSPORT     msg:9ceb8279  node:node-1
  15:51:35.349  TRACE_ACTION_ROUTED_REMOTE         msg:9ceb8279  node:node-1
```

Or programmatically via the `GetTrace` gRPC RPC on the AgentAPI.

> **Note:** `GetTrace` returns spans from the local node only. For cross-node traces, query each node's daemon separately.

## Prometheus Metrics

When `metrics_addr` is configured (default `:9090`), the daemon exposes a `/metrics` endpoint:

```bash
curl http://localhost:9090/metrics
```

**Counters** (read from ActivityBus atomics at scrape time, no double-counting):

| Metric | Description |
|--------|-------------|
| `tailbus_messages_routed_total` | Total messages routed (local + remote) |
| `tailbus_messages_delivered_local_total` | Messages delivered to local subscribers |
| `tailbus_messages_sent_remote_total` | Messages sent to remote peers |
| `tailbus_messages_received_remote_total` | Messages received from remote peers |
| `tailbus_sessions_opened_total` | Total sessions opened |
| `tailbus_sessions_resolved_total` | Total sessions resolved |

**Histograms:**

| Metric | Description |
|--------|-------------|
| `tailbus_message_routing_duration_seconds` | Time to route a message (resolve + deliver/send) |
| `tailbus_session_lifetime_seconds` | Duration from session open to resolve |

To disable metrics, pass `--metrics ""` or set `metrics_addr = ""` in the config file.

## TUI Dashboard

The interactive dashboard provides a real-time view of the local daemon:

```bash
./bin/tailbus dashboard
```

**Panels:**
- **Handles** — registered agent handles with subscriber counts
- **Peers** — remote nodes with connection status
- **Sessions** — open and resolved sessions
- **Activity** — live feed of message routes, session events, and registrations (includes trace ID prefixes)

**Keyboard shortcuts:**
- `q` / `Ctrl+C` — quit
- `r` — refresh status
- `c` — clear activity feed

## Architecture

```
proto/tailbus/v1/           Protocol buffer definitions
  messages.proto              Envelope, EnvelopeType
  agent.proto                 AgentAPI service (local daemon <-> agents)
  coord.proto                 CoordinationAPI service (daemon <-> coord)
  transport.proto             NodeTransport service (daemon <-> daemon P2P)

internal/
  coord/                    Coordination server
    server.go                 gRPC server implementation
    store.go                  SQLite-backed persistence
    registry.go               Node registration
    peermap.go                Peer map distribution

  daemon/                   Node daemon
    daemon.go                 Main orchestrator (wires all components)
    agentserver.go            AgentAPI gRPC server (Unix socket)
    coordclient.go            Coord server gRPC client
    router.go                 Message routing (local vs remote)
    activitybus.go            In-process pub/sub for observability
    tracestore.go             Ring buffer trace span storage
    metrics.go                Prometheus collector + HTTP server

  transport/                P2P data plane
    transport.go              Transport interface
    grpc.go                   Bidirectional gRPC stream implementation

  handle/                   Handle resolution
  session/                  Session lifecycle state machine
  identity/                 Keypair generation and management
  config/                   TOML configuration loading
```

### Message flow

1. Agent calls `OpenSession` / `SendMessage` / `ResolveSession` via Unix socket
2. `AgentServer` creates the envelope, records a trace span, and passes it to `MessageRouter`
3. `MessageRouter` checks if destination handle is local:
   - **Local**: delivers directly to subscriber channels, records `ROUTED_LOCAL` span
   - **Remote**: resolves handle to peer address via `Resolver`, sends via `GRPCTransport`, records `ROUTED_REMOTE` span
4. `GRPCTransport` sends over bidirectional gRPC stream, fires `OnSend` callback (`SENT_TO_TRANSPORT` span)
5. Remote daemon receives via `OnReceive` callback (`RECEIVED_FROM_TRANSPORT` span), delivers to local subscribers (`DELIVERED_TO_SUBSCRIBER` span)

### Protocol

All inter-component communication uses **gRPC** with Protocol Buffers:

- **AgentAPI** — agents connect to their local daemon via **Unix socket**
- **CoordinationAPI** — daemons connect to the coord server via **TCP**
- **NodeTransport** — daemons connect to each other via **TCP** for P2P message exchange

## Building External Agents

External agents connect to the local daemon's Unix socket using any gRPC client. The AgentAPI proto defines the full interface:

```protobuf
service AgentAPI {
  rpc Register(RegisterRequest) returns (RegisterResponse);
  rpc OpenSession(OpenSessionRequest) returns (OpenSessionResponse);
  rpc SendMessage(SendMessageRequest) returns (SendMessageResponse);
  rpc Subscribe(SubscribeRequest) returns (stream IncomingMessage);
  rpc ResolveSession(ResolveSessionRequest) returns (ResolveSessionResponse);
  rpc ListSessions(ListSessionsRequest) returns (ListSessionsResponse);
  rpc GetNodeStatus(GetNodeStatusRequest) returns (GetNodeStatusResponse);
  rpc WatchActivity(WatchActivityRequest) returns (stream ActivityEvent);
  rpc GetTrace(GetTraceRequest) returns (GetTraceResponse);
}
```

Example in Go:

```go
conn, _ := grpc.NewClient("unix:///tmp/tailbusd.sock",
    grpc.WithTransportCredentials(insecure.NewCredentials()))
client := agentpb.NewAgentAPIClient(conn)

// Register
client.Register(ctx, &agentpb.RegisterRequest{Handle: "my-agent"})

// Subscribe to messages
stream, _ := client.Subscribe(ctx, &agentpb.SubscribeRequest{Handle: "my-agent"})
for {
    msg, _ := stream.Recv()
    fmt.Printf("Got: %s\n", msg.Envelope.Payload)
}
```

## Development

```bash
# Run all tests (including integration)
make test-all

# Regenerate proto code after editing .proto files
make proto

# Build all binaries
make build
```

### Running integration tests only

```bash
go test ./internal/ -v -run TestEndToEnd
```

The integration test spins up a full topology (coord server + 2 daemons + 2 agents) in-process and verifies the complete session lifecycle including tracing.

## License

See LICENSE file for details.
