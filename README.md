<h1 align="center" style="border-bottom: none;">Hanzo Cloud</h1>
<h3 align="center">AI Cloud OS — Native Go model routing, IAM-integrated auth, usage tracking, and KMS secrets management. Zero middlemen, pure performance.</h3>
<p align="center">
  <a href="https://github.com/hanzoai/cloud/actions/workflows/build.yml">
    <img alt="Build" src="https://github.com/hanzoai/cloud/workflows/Build/badge.svg?style=flat-square">
  </a>
  <a href="https://github.com/hanzoai/cloud/releases/latest">
    <img alt="Release" src="https://img.shields.io/github/v/release/hanzoai/cloud.svg">
  </a>
  <a href="https://hub.docker.com/r/hanzoai/cloud">
    <img alt="Docker" src="https://img.shields.io/badge/Docker%20Hub-latest-brightgreen">
  </a>
  <a href="https://github.com/hanzoai/cloud/blob/master/LICENSE">
    <img src="https://img.shields.io/github/license/hanzoai/cloud?style=flat-square" alt="license">
  </a>
  <a href="https://discord.gg/5rPsrAzK7S">
    <img alt="Discord" src="https://img.shields.io/discord/1022748306096537660?logo=discord&label=discord&color=5865F2">
  </a>
</p>

## Architecture

Hanzo Cloud implements the **ZAP (Zero-overhead API Protocol)** — a native Go model routing layer that connects users directly to upstream LLM providers with no intermediaries.

```
User → cloud-api (Go, ZAP gateway) → upstream providers (DO-AI, Fireworks, OpenAI)
                ↕                              ↕
           Hanzo IAM                      Hanzo KMS
      (auth, billing, usage)          (multi-tenant secrets)
```

| Component | Description | Technology |
|-----------|-------------|------------|
| **Gateway** | ZAP-native model routing, auth, billing | Go + Beego |
| **Frontend** | Admin UI, chat, knowledge base | React + Next.js |
| **IAM** | Identity, API keys (`hk-`), usage tracking | [hanzoai/iam](https://github.com/hanzoai/iam) |
| **KMS** | Multi-tenant secrets, org-scoped projects | [hanzoai/kms](https://github.com/hanzoai/kms) |
| **Engine** | Local inference (mistral.rs fork) | [hanzoai/engine](https://github.com/hanzoai/engine) |

## ZAP Protocol

ZAP defines the fast native path from API gateway to LLM inference:

- **OpenAI-compatible** JSON over HTTP (`/v1/chat/completions`, `/v1/models`)
- **Three auth modes**: IAM API key (`hk-*`), JWT (hanzo.id OAuth), Provider key (`sk-*`)
- **Static model routing** — 66+ models mapped to 3 upstream providers in pure Go
- **Per-request usage tracking** — async fire-and-forget to IAM
- **KMS-resolved secrets** — provider API keys from Infisical with org-scoped projects
- **Zero Python** — no LiteLLM, no middleware, no extra hops

## Supported Models

### Free Tier (DO-AI) — 28 models
`gpt-4o`, `gpt-5`, `gpt-5-mini`, `claude-opus-4-6`, `claude-sonnet-4-5`, `claude-haiku-4-5`, `o3`, `o3-mini`, `qwen3-32b`, `deepseek-r1-distill-70b`, `llama-3.3-70b`, and more.

### Premium (Fireworks) — 17 models
`fireworks/deepseek-r1`, `fireworks/deepseek-v3`, `fireworks/kimi-k2`, `fireworks/qwen3-235b-a22b`, `fireworks/qwen3-coder-480b`, `fireworks/cogito-671b`, and more.

### Premium (OpenAI Direct) — 5 models
`openai-direct/gpt-4o`, `openai-direct/gpt-5`, `openai-direct/o3`, `openai-direct/o3-mini`, `openai-direct/gpt-4o-mini`

### Zen Models (Premium, 3X) — 8 models
`zen4-mini`, `zen4-pro`, `zen4-max`, `zen4-ultra`, `zen4-coder-flash`, `zen4-coder-pro`, `zen-vl`, `zen3-omni`

Full model list: `GET /api/models`

## Quick Start

```bash
# Build
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o cloud-api-server .

# Run
./cloud-api-server

# Test
curl -H "Authorization: Bearer hk-YOUR-API-KEY" \
  https://api.hanzo.ai/v1/chat/completions \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"Hello"}]}'
```

## Configuration

Set via `conf/app.conf` or environment variables:

| Variable | Description |
|----------|-------------|
| `IAM_URL` | IAM service URL (default: `http://iam.hanzo.svc.cluster.local:8000`) |
| `KMS_CLIENT_ID` | Infisical Universal Auth client ID |
| `KMS_CLIENT_SECRET` | Infisical Universal Auth client secret |
| `KMS_PROJECT_ID` | Default KMS project ID |
| `KMS_ENVIRONMENT` | KMS environment (default: `production`) |

## Deploy

```bash
# Docker
docker pull ghcr.io/hanzoai/cloud:latest

# Kubernetes
kubectl apply -f k8s/
```

## License

[Apache-2.0](https://github.com/hanzoai/cloud/blob/master/LICENSE)
