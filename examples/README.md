# Examples

This directory contains standalone YAML manifests for Sympozium CRDs. Each file includes instructions at the top.

## Quick Start (Copy-Paste)

```bash
# 1. Create OpenAI secret
kubectl create secret generic my-openai-key --from-literal=key=sk-...

# 2. Apply quickstart example
kubectl apply -f yaml/quickstart.yaml

# 3. Watch it run
kubectl get agentrun quickstart-run -w
```

## Directory Structure

```
examples/
├── README.md              # This file (quick reference)
├── SETUP.md               # Detailed setup guide (secrets, prerequisites)
└── yaml/                  # YAML manifests (copy-paste ready)
    ├── quickstart.yaml               # All-in-one quick start
    ├── ensemble-example.yaml      # Team of agents
    ├── agent-example.yaml            # Single agent
    ├── agentrun-example.yaml         # One-off task
    ├── sympoziumschedule-example.yaml  # Recurring task
    ├── ensemble-activate.yaml     # Activation examples
    ├── agentrun-trigger.yaml         # Multiple trigger methods
    ├── skillpack-example.yaml        # Custom skills
    └── sympoziumpolicy-example.yaml  # Tool access rules
```

## Examples Reference

| Resource | Purpose | When to Use |
|----------|---------|-------------|
| **Ensemble** | Bundle multiple personas with skills & schedules | Setting up teams of agents |
| **Agent** | Single agent with channels & auth | Custom single-agent setups |
| **AgentRun** | One-off task execution | Ad-hoc requests, testing |
| **SympoziumSchedule** | Recurring heartbeat/cron tasks | Monitoring, periodic checks |
| **SkillPack** | Custom skill definitions | Extend agent capabilities |
| **SympoziumPolicy** | Tool access control rules | Security, compliance |

## Common Workflows

### Deploy a Team of Agents

```bash
kubectl apply -f yaml/ensemble-example.yaml
sympozium  # Select Ensembles tab, press Enter on pack name
```

### Run a One-Off Task

```bash
kubectl apply -f yaml/agentrun-example.yaml
```

### Set Up Monitoring

```bash
kubectl apply -f yaml/agent-example.yaml
kubectl apply -f yaml/sympoziumschedule-example.yaml
```

## See Also

- [Setup Guide](./SETUP.md) - Detailed prerequisites and secrets setup
- [Getting Started Guide](../docs/getting-started.md)
- [Sample CRs in config/samples/](../config/samples/)
