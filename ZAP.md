# ZAP — Zero-overhead API Protocol

**Version 1.0** | Hanzo AI

## Overview

ZAP is Hanzo's native protocol for high-performance LLM inference routing. It defines the contract between clients, the API gateway, upstream providers, and supporting services (IAM, KMS). ZAP is designed for zero unnecessary hops — every request takes the shortest path from client to model.

```
Client ──ZAP──> cloud-api (Go) ──ZAP──> upstream provider
                    │                         │
                    ├── IAM (auth/billing)     ├── DO-AI (OpenAI-compatible)
                    └── KMS (secrets)          ├── Fireworks (OpenAI-compatible)
                                              └── OpenAI Direct
```

## Design Principles

1. **Zero overhead** — No Python, no middleware proxies, no YAML configs at runtime
2. **OpenAI-compatible** — Drop-in replacement for OpenAI API clients
3. **Static routing** — Model→provider mapping compiled into the binary
4. **Auth-agnostic** — Supports API keys, JWT, and raw provider keys
5. **Async observability** — Usage tracking never blocks inference
6. **MCP-native** — Engine exposes MCP server; Node connects as MCP client

## Endpoints

### Inference

```
POST /v1/chat/completions    # Chat (streaming + non-streaming)
POST /v1/completions         # Text completions
POST /v1/embeddings          # Embeddings
GET  /v1/models              # List available models
GET  /api/models             # List with routing metadata
```

### Auth (via Hanzo IAM)

```
GET  /api/get-user?accessKey=hk-...   # Resolve API key to user
POST /api/add-usage-record            # Record usage (async)
GET  /api/get-usage-summary           # Aggregated billing
GET  /api/get-usage-records           # Detailed usage log
```

## Authentication

ZAP supports three auth modes via `Authorization: Bearer <token>`:

| Mode | Token Format | Resolution | Use Case |
|------|-------------|------------|----------|
| **IAM API Key** | `hk-{uuid}` | IAM `GetUserByAccessKey` → user/org → model routing | Console users, SDK |
| **JWT** | `eyJ...` | IAM JWT validation → user/org → model routing | Web apps, OAuth |
| **Provider Key** | `sk-...` | Direct provider lookup → bypass routing | Power users |

### IAM API Key Flow

```
1. Client sends: Authorization: Bearer hk-abc123...
2. Gateway calls: IAM GET /api/get-user?accessKey=hk-abc123
3. IAM returns: { user, organization, balance }
4. Gateway checks: balance > 0 (if premium model)
5. Gateway routes: model → provider via static map
6. Gateway resolves: provider API key via KMS
7. Gateway proxies: request to upstream with translated model name
8. Gateway records: usage async to IAM POST /api/add-usage-record
```

## Model Routing

Models are resolved via a static Go map compiled into the binary:

```go
type modelRoute struct {
    providerName  string  // "do-ai", "fireworks", "openai-direct"
    upstreamModel string  // Model ID sent to upstream API
    premium       bool    // Requires positive balance
}
```

### Provider Types

| Provider | Base URL | Auth | Free Tier |
|----------|----------|------|-----------|
| `do-ai` | `https://inference.do-ai.run/v1` | Bearer token | Yes |
| `fireworks` | `https://api.fireworks.ai/inference/v1` | Bearer token | No |
| `openai-direct` | `https://api.openai.com/v1` | Bearer token | No |

### Model Translation

Client sends `model: "gpt-4o"` → Gateway translates to `openai-gpt-4o` for DO-AI upstream.
Client sends `model: "zen4-pro"` → Gateway translates to `accounts/fireworks/models/qwen3-235b-a22b` for Fireworks.

## Usage Tracking

Every request generates a `UsageRecord` sent async to IAM:

```json
{
  "owner": "org/hanzo",
  "name": "auto-generated-uuid",
  "user": "org/username",
  "model": "gpt-4o",
  "provider": "do-ai",
  "promptTokens": 150,
  "completionTokens": 280,
  "totalTokens": 430,
  "cost": 0.00215,
  "premium": false,
  "stream": true,
  "status": "success",
  "requestId": "req-abc123",
  "clientIp": "203.0.113.42"
}
```

## KMS Integration

Provider API keys are stored in Infisical (Hanzo KMS) and resolved at runtime:

```
Provider.ClientSecret = "kms://DO_AI_API_KEY"
  → KMS resolves to actual API key
  → Cached for 5 minutes
  → Org-scoped via Provider.ConfigText = "kms-project:{project-id}"
```

## Relationship to MCP

ZAP and MCP serve different layers:

| | ZAP | MCP |
|---|---|---|
| **Purpose** | Fast inference routing | Tool/context protocol |
| **Transport** | HTTP JSON (OpenAI format) | JSON-RPC (stdio/HTTP/WS) |
| **Scope** | Gateway → Provider | Agent → Tools/Resources |
| **Implementation** | cloud-api (Go) | engine (Rust), node (Rust) |

Engine exposes an MCP server on `--mcp-port`. Node connects to Engine as MCP client via `hanzo-mcp`. ZAP handles the inference layer; MCP handles the tool layer. Together they form the complete Hanzo AI stack.

## Service Architecture

```
                    ┌─────────────────────────┐
                    │     Client / SDK        │
                    │  Authorization: Bearer  │
                    │  hk-... / eyJ... / sk-  │
                    └───────────┬─────────────┘
                                │ ZAP (HTTP JSON)
                    ┌───────────▼─────────────┐
                    │    cloud-api (Go)        │
                    │    ZAP Gateway           │
                    │  ┌─────────────────┐     │
                    │  │ Model Router    │     │
                    │  │ (static Go map) │     │
                    │  └────────┬────────┘     │
                    │           │              │
                    │  ┌────────▼────────┐     │
                    │  │ Provider Pool   │     │
                    │  │ (cached + KMS)  │     │
                    │  └────────┬────────┘     │
                    └───────────┼──────────────┘
                     │          │           │
              ┌──────▼───┐ ┌───▼────┐ ┌────▼──────┐
              │  DO-AI   │ │Firework│ │  OpenAI   │
              │ (28 free)│ │(17 prm)│ │ (5 prem)  │
              └──────────┘ └────────┘ └───────────┘

    ┌──────────┐                          ┌──────────┐
    │ Hanzo    │◄── auth, billing ───────►│ Hanzo    │
    │ IAM     │    usage tracking         │ KMS     │
    │ (Go)    │                           │(Infiscl)│
    └──────────┘                          └──────────┘

    ┌──────────────────────────────────────────────┐
    │           Hanzo Engine (Rust)                 │
    │  Local inference, MCP server                 │
    │  ← MCP client (Node) connects here           │
    └──────────────────────────────────────────────┘

    ┌──────────────────────────────────────────────┐
    │           Hanzo Node (Rust)                   │
    │  AI agent platform, MCP client               │
    │  100+ LLM providers, tool execution          │
    └──────────────────────────────────────────────┘
```

## Implementation

- **Gateway**: [`hanzoai/cloud`](https://github.com/hanzoai/cloud) — `controllers/openai_api.go`, `controllers/model_routes.go`
- **IAM**: [`hanzoai/iam`](https://github.com/hanzoai/iam) — `object/usage.go`, `controllers/usage.go`
- **KMS**: [`hanzoai/kms`](https://github.com/hanzoai/kms) — Infisical fork
- **Engine**: [`hanzoai/engine`](https://github.com/hanzoai/engine) — mistral.rs fork with MCP server
- **Node**: [`hanzoai/node`](https://github.com/hanzoai/node) — AI agent platform with MCP client
