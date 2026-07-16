<p align="center"><img src=".github/hero.svg" alt="ai" width="880"></p>

# ai

LLM control plane, RAG, and model hub for the Hanzo platform. Native Go model routing with no Python middlemen.

[![Status](https://img.shields.io/badge/status-stable-green)]()
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)]()

Forked from [Casibase](https://github.com/casibase/casibase) (Apache-2.0).

## Quick start

```bash
docker pull ghcr.io/hanzoai/ai:latest
```

```bash
curl -H "Authorization: Bearer hk-YOUR-API-KEY" \
  https://api.hanzo.ai/v1/chat/completions \
  -d '{"model":"zen4-pro","messages":[{"role":"user","content":"Hello"}]}'
```

## What this is

`ai` is the canonical LLM control plane for the Hanzo platform — model routing, RAG, model hub, MCP management. OpenAI-compatible JSON over HTTP (`/v1/chat/completions`, `/v1/models`). Three auth modes (IAM API key `hk-*`, JWT, provider key `sk-*`). Static model routing — 66+ models mapped to upstream providers in pure Go. Per-request usage tracking fire-and-forget to IAM. KMS-resolved provider secrets, org-scoped. Renamed from `hanzoai/cloud` on 2026-05-19 when the unified binary took the `cloud` name per HIP-0106; `ai` mounts as the `ai` subsystem inside `hanzoai/cloud`.

## Specs

Implements:
- HIP-0037 AI Cloud Platform
- HIP-0106 Unified Cloud Binary (ai subsystem)

## Architecture

```
   user / api key  ->  hanzoai/gateway  ->  hanzoai/ai (zip.App, Go)
                                                  |
                              auth: hk-* | JWT | sk-* (provider passthrough)
                                                  |
                              model routing: 66+ models -> upstream providers
                                                  |
                          +--------+--------+--------+--------+
                          |        |        |        |        |
                       DO-AI  Fireworks  OpenAI   Anthropic   ...
                          |        |        |        |        |
                              KMS-resolved provider secrets
                                                  |
                              IAM usage tracking (async)
```


---

<h1 align="center" style="border-bottom: none;">Hanzo Cloud</h1>
<h3 align="center">AI Cloud OS — Native Go model routing, IAM-integrated auth, usage tracking, and KMS secrets management. Zero middlemen, pure performance.</h3>
<p align="center">
  <a href="https://github.com/hanzoai/cloud/actions/workflows/build.yml">
    <img alt="Build" src="https://github.com/hanzoai/cloud/workflows/Build/badge.svg?style=flat-square">
  </a>
  <a href="https://github.com/hanzoai/cloud/releases/latest">
    <img alt="Release" src="https://img.shields.io/github/v/release/hanzoai/cloud.svg">
  </a>
  <a href="https://github.com/hanzoai/cloud/pkgs/container/cloud">
    <img alt="GHCR" src="https://img.shields.io/badge/GHCR-latest-brightgreen">
  </a>
  <a href="https://github.com/hanzoai/cloud/blob/main/LICENSE">
    <img src="https://img.shields.io/github/license/hanzoai/cloud?style=flat-square" alt="license">
  </a>
  <a href="https://discord.gg/5rPsrAzK7S">
    <img alt="Discord" src="https://img.shields.io/discord/1022748306096537660?logo=discord&label=discord&color=5865F2">
  </a>
</p>

## Architecture

Hanzo Cloud implements the **ZAP (Zero-overhead API Protocol)** — a native Go model routing layer that connects users directly to upstream AI providers with no intermediaries.

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

ZAP defines the fast native path from API gateway to AI inference:

- **OpenAI-compatible** JSON over HTTP (`/v1/chat/completions`, `/v1/models`)
- **Three auth modes**: IAM API key (`hk-*`), JWT (hanzo.id OAuth), Provider key (`sk-*`)
- **Static model routing** — 66+ models mapped to 3 upstream providers in pure Go
- **Per-request usage tracking** — async fire-and-forget to IAM
- **KMS-resolved secrets** — provider API keys from Hanzo KMS with org-scoped projects
- **Zero Python** — no legacy proxy middleware, no extra hops

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

## Auto-routing (`auto` / `zen-router`)

Opt-in virtual model that picks the best concrete model per request. Send
`"model": "auto"` (alias `"zen-router"`) and the request is classified into a
coarse task (code, reasoning, math, creative, vision, long-context, cheap-chat,
general) and mapped to the first **servable** model in the matching preference
list — *before* provider, pricing, and billing resolution. So an `auto` request
is billed and reported as exactly the model that served it.

Enable it in `conf/models.yaml` (the `cloud-api-models` ConfigMap in prod), or
via env `ROUTER_ENABLED=true` / `ROUTER_ENDPOINT=<url>`:

```yaml
router:
  enabled: true
  endpoint: ""          # optional zen-router URL (see below); "" = built-in heuristic
  cost_ceiling: 0.0
  prefer:
    code:       [zen4-coder, zen5-coder, qwen3-coder, gpt-5.3-codex]
    reasoning:  [zen4-thinking, deepseek-reasoner, zen4-ultra]
    cheap_chat: [zen4-mini, zen5-mini, gpt-4o-mini]
    default:    [zen4, zen5, gpt-4o]
```

```bash
curl -H "Authorization: Bearer hk-YOUR-API-KEY" \
  -H "X-Max-Cost: 2.0" -H "X-Max-Latency-Ms: 800" \
  https://api.hanzo.ai/v1/chat/completions \
  -d '{"model":"auto","messages":[{"role":"user","content":"refactor this function"}]}'
# → response header:  X-Routed-Model: zen4-coder
# → response body:    {"model":"zen4-coder", ...}
```

### Per-org enable/disable

Routing can be overridden per org via the admin `OrgSettings` surface
(`/v1/*-org-settings`, global-admin-gated like `/v1/*-model-route`). An org row
carries `autoRouting`: `""` (unset), `"enabled"`, or `"disabled"`. It blends with
the global `router.enabled` flag as follows (`AutoRoutingActive`):

| global `router.enabled` | org `""` (unset) | org `"enabled"` | org `"disabled"` |
|-------------------------|------------------|-----------------|------------------|
| **true**                | route            | route           | **off** (no rewrite, no header) |
| **false**               | off              | route¹          | off              |

¹ Per-org opt-in routes even when the global flag is off, but ONLY when router
config is present (a `prefer` table or an `endpoint`) — otherwise there is nothing
to route with and `auto` is left unchanged.

When routing is not active for an org, an `auto`/`zen-router` request is left
untouched — no `X-Routed-Model` header, exactly its pre-routing behavior. Set an
org's preference with, e.g.:

```bash
curl -X POST "$CLOUD_ADMIN/v1/update-org-settings?owner=<org>" \
  -H "Content-Type: application/json" -b "$ADMIN_SESSION" \
  -d '{"owner":"<org>","autoRouting":"disabled"}'
```

- **Transparency**: the `X-Routed-Model` response header (and the `model` field
  in the body) report the model that served the request.
- **SLO** (optional): `X-Max-Cost` (per-1k, float) and `X-Max-Latency-Ms` (int)
  headers express a per-request budget, forwarded to the engine strategy.
- **Two strategies**: with `endpoint` set, ai POSTs `{prompt, tasks?, slo}` to
  `{endpoint}/route` and uses its `{model, task, confidence}` reply — a learned
  **zen-router** ([huggingface.co/zenlm/zen-router](https://huggingface.co/zenlm/zen-router))
  served OpenAI-compatibly by [hanzo-engine](https://github.com/hanzoai/engine).
  On **any** error (or with no endpoint) it falls back to a built-in pure-Go
  heuristic (a faithful port of hanzo-router's classifier), so `auto` works with
  **zero extra infrastructure**. The learned zen-router is the quality upgrade
  over the heuristic — same interface, better routing.
- **Per-org**: routing enable is deployment-global (there is no per-org settings
  object yet); the *resolved* model id still goes through per-org `ModelRoute`
  resolution and pricing, so orgs keep their overrides for whatever `auto` picks.

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
| `HANZO_API_KEY` | Unified service token for internal IAM + KMS operations (billing/usage/auth support) |
| `iamEndpoint` | IAM service URL (production: `http://iam.hanzo.svc.cluster.local:8000`) |
| `clientId` | IAM OAuth client ID for cloud |
| `clientSecret` | IAM OAuth client secret for cloud |
| `dataSourceName` | Database DSN (do not commit; inject via KMS-managed secret) |
| `KMS_CLIENT_ID` | KMS Universal Auth client ID |
| `KMS_CLIENT_SECRET` | KMS Universal Auth client secret |
| `KMS_PROJECT_ID` | Default KMS project ID |
| `KMS_ENVIRONMENT` | KMS environment (default: `production`) |

## Deploy

```bash
# Docker
docker pull ghcr.io/hanzoai/cloud:latest

# Kubernetes (production)
kubectl apply -f k8s/kms-secrets.yaml
kubectl apply -f k8s/configmap.yaml
kubectl apply -f k8s/deployment.yaml
kubectl apply -f k8s/service.yaml
```

GitHub Actions production deploy (`.github/workflows/deploy-production.yml`) resolves deployment credentials from Hanzo KMS. Preferred setup is a single GitHub secret: `HANZO_API_KEY`. Universal Auth (`KMS_CLIENT_ID` + `KMS_CLIENT_SECRET`) remains as a fallback.

## License

[Apache-2.0](https://github.com/hanzoai/cloud/blob/main/LICENSE)
