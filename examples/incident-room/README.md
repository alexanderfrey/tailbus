# Incident Room

This is the Tailbus flagship example.

It shows the product boundary clearly:

- different agents on different nodes
- different departments in one shared conversation
- capability discovery instead of hardcoded routing
- one room as the replayable incident bridge
- live dashboard visibility across the mesh

## What happens

1. The user reports an incident to `@support-triage`
2. `@support-triage` discovers `@incident-orchestrator` by capability
3. `@incident-orchestrator` discovers the right specialists by capability:
   - `ops.logs.search`
   - `ops.metrics.query`
   - `ops.release.history`
   - `billing.account.lookup`
   - `statuspage.compose`
4. The orchestrator creates a shared room and runs the investigation there
5. Specialists reply into the same room from different department nodes
6. The orchestrator returns:
   - an internal incident summary
   - a customer-facing status update
   - a saved room transcript

## Topology

- `support-node`
  - `support-triage`
  - `incident-orchestrator`
- `ops-node`
  - `logs-agent`
  - `metrics-agent`
  - `release-agent`
- `finance-node`
  - `billing-agent`
- `comms-node`
  - `status-agent`

Each node has its own daemon. Tailbus routes discovery, room traffic, and dashboard activity across the mesh.

## Run

### Default deterministic mode

This mode has no external LLM dependency. It is the stable out-of-the-box demo.

```bash
cd /Users/alexanderfrey/Projects/tailbus
make build
cd examples/incident-room
./run.sh
```

Open the dashboard:

```bash
tailbus -socket /tmp/incidentroom-support-node.sock dashboard
```

Open an incident:

```bash
./run.sh fire "EU customers cannot complete checkout"
./run.sh fire "Subscriptions are paid but not activating"
```

Watch logs:

```bash
./run.sh logs
```

Stop:

```bash
./run.sh stop
```

### LLM-backed mode

This variant keeps the same incident-room flow, but adds:

- `lmstudio-analyst` for room-level root-cause synthesis
- `codex-status-agent` for customer-facing update drafting

Prerequisites:

- LM Studio running locally at `http://localhost:1234/v1`
- `codex` CLI installed and authenticated
- a small OpenAI model available to Codex

Run it:

```bash
cd /Users/alexanderfrey/Projects/tailbus
make build
cd examples/incident-room
./run-llm.sh
```

Useful environment overrides:

```bash
LLM_BASE_URL=http://localhost:1234/v1
LLM_MODEL=your-local-model
CODEX_MODEL=gpt-5-mini
CODEX_TIMEOUT=90
```

## Capability discovery

This example intentionally uses discovery, not fixed handles.

Inspect the mesh:

```bash
tailbus -socket /tmp/incidentroom-support-node.sock list --verbose
tailbus -socket /tmp/incidentroom-support-node.sock find --capabilities incident.orchestrate
tailbus -socket /tmp/incidentroom-support-node.sock find --capabilities ops.logs.search
```

In LLM mode you can also inspect:

```bash
tailbus -socket /tmp/incidentroom-support-node.sock find --capabilities incident.analyze
tailbus -socket /tmp/incidentroom-support-node.sock find --capabilities statuspage.compose
```

## Output

Transcripts are written to:

[`examples/incident-room/output`](/Users/alexanderfrey/Projects/tailbus/examples/incident-room/output)

Each transcript includes:

- discovered specialists and match reasons
- internal incident summary
- customer-facing status update
- room event transcript

## Why this demo matters

Most agent demos stop at one runtime and one orchestrator.

This one shows what Tailbus is actually for:

- heterogeneous agent systems connected over a mesh
- cross-department collaboration without one giant runtime
- daemon-managed rooms as shared conversation state
- capability-driven specialist lookup
- observability in the dashboard while the incident is live

The LLM-backed variant adds one more important point:

- classic specialist agents and LLM-backed agents can collaborate in the same Tailbus room without sharing a runtime
