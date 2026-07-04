# Setup Guide

Before applying any examples, you need to set up prerequisites.

## 1. Install Sympozium

```bash
# Via brew (macOS/Linux)
brew install sympozium-ai/sympozium/sympozium

# OR via shell installer
curl -fsSL https://deploy.sympozium.ai/install.sh | sh

# Verify install
sympozium version
```

## 2. Install Sympozium to Your Cluster

```bash
sympozium install
```

This creates:
- CRDs (Custom Resource Definitions)
- Controller, API server, webhook
- NATS event bus
- Default policies

## 3. Create Required Secrets

Examples use these secret patterns:

### OpenAI API Key
```bash
kubectl create secret generic my-openai-key \
  --from-literal=key=sk-your-actual-openai-api-key
```

### Telegram Bot Token
```bash
kubectl create secret generic alice-telegram-secret \
  --from-literal=TELEGRAM_BOT_TOKEN=123456789:ABC-DEF1234ghIkl-zyx57W2v1u123ew11
```

### Slack Bot Token
```bash
kubectl create secret generic alice-slack-secret \
  --from-literal=botToken=xoxb-...
```

### GitHub Token
```bash
kubectl create secret generic alice-github-token \
  --from-literal=token=ghp_xxxxxxxxxxxx
```

## 4. Verify Prerequisites

```bash
# Check CRDs are installed
kubectl get crd | grep sympozium.ai

# Check default policies exist
kubectl get sympoziumpolicy

# Check secrets are created
kubectl get secret my-openai-key
```

## 5. Apply an Example

```bash
# Quick start (all-in-one)
kubectl apply -f yaml/quickstart.yaml

# OR individual resources
kubectl apply -f yaml/personapack-example.yaml
kubectl apply -f yaml/sympoziuminstance-example.yaml
```

## Common Issues

### Secret Not Found

**Error:** `secret "my-openai-key" not found`

**Fix:** Create the secret before applying:
```bash
kubectl create secret generic my-openai-key --from-literal=key=sk-...
```

### Policy Not Found

**Error:** `sympoziumpolicy "default-policy" not found`

**Fix:** Re-run install:
```bash
sympozium install
```

### CRDs Not Found

**Error:** `no matches for kind "Agent"`

**Fix:** Wait for CRDs to be registered (30-60 seconds after install):
```bash
kubectl get crd | grep sympozium.ai
```

## Next Steps

- Try the [Quick Start](#quick-start) example
- Explore other examples in `yaml/`
- Read [Getting Started](../docs/getting-started.md) for more details
