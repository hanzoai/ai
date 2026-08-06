<p align="center"><img src=".github/hero.svg" alt="Hanzo AI" width="880"></p>

# ai

`ai` is the AI subsystem of the Hanzo cloud: it serves the `/v1` inference surface, routes
each request to a model, meters it, and carries the RAG and model-hub surfaces alongside.
Pure Go.

[![Release](https://img.shields.io/github/v/release/hanzoai/ai.svg)](https://github.com/hanzoai/ai/releases/latest)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)

It used to be the whole `hanzoai/cloud` repository. When the unified binary took the
`cloud` name (HIP-0106), this became `ai` and now mounts as the `ai` subsystem inside it.
That is why the root of this repository is `package ai`, a library, and not a `main`.

## Call it

The hosted instance is `api.hanzo.ai`. Get a key at [hanzo.ai](https://hanzo.ai):

```bash
curl -H "Authorization: Bearer sk-YOUR-API-KEY" \
  https://api.hanzo.ai/v1/chat/completions \
  -d '{"model":"zen5","messages":[{"role":"user","content":"Hello"}]}'
```

Three auth modes are accepted: a Hanzo API key (`sk-*`), a JWT from hanzo.id OAuth, or a
provider key you already hold, in which case the request is routed with your account
rather than metered on ours.

## Run it yourself

```bash
docker run -p 8000:8000 ghcr.io/hanzoai/ai:vX.Y.Z    # pin a release, never :latest
```

Or build the server binary. It is `cmd/aid` — building the repository root gives you a
library archive, not something you can run:

```bash
CGO_ENABLED=0 go build -o aid ./cmd/aid
./aid -h
```

`compose.yml` brings up the server with its Postgres.

## Models

The gateway owns the catalog; this service does not keep a second list. Ask it:

```bash
curl -H "Authorization: Bearer sk-YOUR-API-KEY" https://api.hanzo.ai/v1/models
```

`https://catalog.hanzo.ai/v1/models` answers the same question without a key. Zen is our
own family — `enso`, `enso-ultra` and the `zen5` line, `owned_by: hanzo`. Models you
connect with your own provider keys are routed as well, resolved from Hanzo KMS and scoped
to your org.

## Auto-routing

Send `"model": "auto"` (alias `"zen-router"`) and the request is classified into a coarse
task — code, reasoning, math, creative, vision, long-context, cheap-chat, general — and
mapped to the first servable model in the matching preference list, before pricing and
billing resolve. So an `auto` request is billed and reported as exactly the model that
served it.

Turn it on in `conf/models.yaml` (the `cloud-api-models` ConfigMap in production), or with
`ROUTER_ENABLED=true` / `ROUTER_ENDPOINT=<url>`:

```yaml
router:
  enabled: true
  endpoint: ""          # "" = built-in heuristic; a URL = the learned router
  cost_ceiling: 0.0
  prefer:
    code:       [zen5-coder]
    reasoning:  [enso-ultra, enso]
    cheap_chat: [zen5-mini, zen5-flash]
    default:    [zen5]
```

```bash
curl -H "Authorization: Bearer sk-YOUR-API-KEY" \
  -H "X-Max-Cost: 2.0" -H "X-Max-Latency-Ms: 800" \
  https://api.hanzo.ai/v1/chat/completions \
  -d '{"model":"auto","messages":[{"role":"user","content":"refactor this function"}]}'
# response header: X-Routed-Model: <the model that served it>
```

`X-Routed-Model` and the `model` field in the body always report what actually ran.
`X-Max-Cost` (per 1k, float) and `X-Max-Latency-Ms` (int) express a per-request budget.

Two strategies sit behind one interface. With `endpoint` set, `ai` POSTs
`{prompt, tasks?, slo}` to `{endpoint}/route` and takes its `{model, task, confidence}`
reply — that is the learned router,
[zenlm/zen-router](https://huggingface.co/zenlm/zen-router). On any error, or with no
endpoint configured, it falls back to a built-in Go heuristic, so `auto` works with no
extra infrastructure.

### Per-org override

Orgs can opt in or out through the admin `OrgSettings` surface (`/v1/*-org-settings`,
global-admin-gated like `/v1/*-model-route`). The org row carries `autoRouting`: `""`
(unset), `"enabled"` or `"disabled"`, and blends with the global flag:

| global `router.enabled` | org `""` | org `"enabled"` | org `"disabled"` |
|---|---|---|---|
| **true** | route | route | off — no rewrite, no header |
| **false** | off | route¹ | off |

¹ Per-org opt-in routes even when the global flag is off, but only when router config is
present (a `prefer` table or an `endpoint`) — otherwise there is nothing to route with and
`auto` is left unchanged.

## Configuration

`conf/app.conf`, or environment variables:

| Variable | Description |
|----------|-------------|
| `HANZO_API_KEY` | Service token for internal IAM and KMS calls (billing, usage, auth) |
| `iamEndpoint` | IAM service URL |
| `clientId` / `clientSecret` | IAM OAuth client credentials for `ai` |
| `dataSourceName` | Database DSN — inject from KMS, never commit it |
| `KMS_CLIENT_ID` / `KMS_CLIENT_SECRET` | KMS Universal Auth credentials |
| `KMS_PROJECT_ID` | Default KMS project |
| `KMS_ENVIRONMENT` | KMS environment (default `production`) |

Secrets come from [Hanzo KMS](https://github.com/luxfi/kms); the deploy workflow resolves
them at run time from a single `HANZO_API_KEY` GitHub secret, with Universal Auth as the
fallback.

> `ai` runs single-replica: its balance ledger is an in-pod invariant. Deploy with
> `strategy: Recreate` and `min=max=1` — a boot assertion panics if `CLOUD_API_REPLICAS > 1`.
> [`LLM.md`](LLM.md) has the scale-out path.

## Development

```bash
go build -race ./...                       # build
go test $(go list ./...) -tags skipCi      # test (needs MySQL)
golangci-lint run                          # lint

cd web && yarn install && yarn start       # the admin UI
```

[`LLM.md`](LLM.md) is the deep reference — architecture, the mount seam into cloud, and the
conventions that apply here. [`ZAP.md`](ZAP.md) covers the transport.

## Lineage

[`NOTICE`](NOTICE). The routing, auth, billing and model surfaces are ours; the admin UI
and knowledge-base scaffolding started there.

## License

Apache-2.0 — see [LICENSE](LICENSE).

---

Hanzo — the open AI cloud. [hanzo.ai](https://hanzo.ai) · [docs.hanzo.ai](https://docs.hanzo.ai)

SDKs: [Python](https://github.com/hanzoai/python-sdk) · [TypeScript](https://github.com/hanzo-js/sdk) · [Go](https://github.com/hanzo-go/sdk) · [Rust](https://github.com/hanzo-rs/sdk) · [C++](https://github.com/hanzo-cpp/sdk) · [Swift](https://github.com/hanzo-swift/sdk) · [Kotlin](https://github.com/hanzo-kt/sdk) · [umbrella](https://github.com/hanzoai/sdk)
