# Pair Solver

This example shows how Tailbus rooms change the collaboration model.

The orchestrator still controls turn-taking, but it no longer forwards copied history between agents. Instead:

- the orchestrator creates one shared room
- `codex-solver` and `lmstudio-solver` are added as room members
- each turn is posted into the room as a structured `turn_request`
- each solver rebuilds context by replaying the room transcript
- the final markdown output is generated from room replay

## Why this demo matters

It demonstrates the new room primitive directly:

- shared transcript instead of stitched request payloads
- ordered multi-agent collaboration across daemon boundaries
- replayable room history
- orchestrated turns without pretending rooms replace control flow

## Run

```bash
cd /Users/alexanderfrey/Projects/tailbus
make build
cd examples/pair-solver
bash run.sh
bash run.sh fire "Write a Python function that finds the longest palindromic substring"
```

Dashboard:

```bash
tailbus -socket /tmp/pairsolver-orchestrator.sock dashboard
```

Logs:

```bash
bash run.sh logs
```

Stop:

```bash
bash run.sh stop
```

## Environment

- `MAX_ROUNDS`
  Default: `2`
- `TURN_TIMEOUT`
  Default: `180`
- `CODEX_TIMEOUT`
  Default: `180`
- `LLM_BASE_URL`
  Default: `http://localhost:1234/v1`
- `LLM_MODEL`
  Optional LM Studio model override

## Current tradeoff

Rooms provide shared state and replay. The orchestrator still decides whose turn it is. That is intentional: rooms solve conversation state, not coordination policy.

## What to expect in the dashboard

- the `ROOMS` section shows the active pair-solver room
- room activity shows `PROBLEM`, `TURN`, `REPLY`, `TIMEOUT`, and `FINAL` events
- the `WORK` section shows the currently active solver turn while Codex or LM Studio is still thinking
- if you restart the local daemon, the dashboard reconnects automatically once it comes back
