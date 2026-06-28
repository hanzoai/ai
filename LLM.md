# Hanzo Cloud - Claude Code Guide

## Project Overview

Hanzo Cloud is an enterprise-level AI knowledge base and MCP (Model Context Protocol) / A2A (Agent-to-Agent) management platform. It supports 30+ AI model providers (OpenAI, Claude, Gemini, Ollama, etc.) with admin UI, user management, and SSO via Hanzo IAM.

## Architecture

Full-stack application:
- **Backend:** Go 1.23.6 + Beego framework (MVC), MySQL/MariaDB
- **Frontend:** React + Ant Design v5, located in `web/`
- **Auth:** Hanzo IAM SSO integration

## Directory Structure

```
cloud/
├── main.go                 # Entry point
├── go.mod                  # Go module
├── conf/app.conf           # Beego config
├── controllers/            # HTTP handlers (60+ files)
├── object/                 # Business logic & data models (85+ files)
├── model/                  # AI model provider integrations (38+ files)
├── routers/                # Routes and middleware filters
├── embedding/              # Embedding provider implementations
├── agent/                  # MCP agent support
├── split/                  # Text splitting strategies
├── txt/                    # Document parsing (PDF, CSV, XLSX, PPTX)
├── storage/                # File storage abstractions
├── util/                   # Utilities
└── web/                    # React frontend
    ├── src/
    └── package.json
```

## Build & Run

### Backend
```bash
# Build
go build -race -ldflags "-extldflags '-static'"

# Cross-platform build (linux amd64/arm64/riscv64)
./build.sh
```

### Frontend
```bash
cd web
yarn install
yarn start       # Dev server
yarn run build   # Production build
```

### Docker
```bash
docker compose up
```

## Testing

```bash
# Go tests (requires MySQL)
go test -v $(go list ./...) -tags skipCi

# Frontend tests
cd web && yarn test
```

## Linting

```bash
# Go - uses golangci-lint with gofumpt
golangci-lint run

# Frontend
cd web && yarn lint
```

## Key Conventions

### Backend (Go)
- Framework: Beego MVC - controllers handle HTTP, objects contain business logic
- New AI providers go in `model/` (implement the provider interface)
- Database access via Beego ORM in `object/`
- Route registration in `routers/router.go`
- i18n strings: avoid duplicate keys across frontend and backend

### Frontend (React)
- UI components use Ant Design v5
- i18n via i18next; translation files in `web/src/locales/`
- State via React Context/Hooks (no Redux)
- API calls go through `web/src/backend/` helper modules

### Commits & PRs
- Branch naming: `claude/<name>`
- Main branch: `main`
- Follows semantic versioning (`.releaserc.json`)
- PR titles should be semantic (feat/fix/chore/docs/refactor)

## CI/CD (GitHub Actions)

`.github/workflows/build.yml` runs:
1. Go unit tests (with MySQL service)
2. Frontend build (Node.js 20 + Yarn)
3. Go binary build
4. golangci-lint
5. Semantic release + Docker push to `ghcr.io/hanzoai/cloud`

## Configuration

- Backend config: `conf/app.conf` (Beego format)
- Database: MySQL 8.0+ or MariaDB
- Default Docker DB: PostgreSQL via `compose.yml`

## Important Files

| File | Purpose |
|------|---------|
| `main.go` | Application entry point |
| `routers/router.go` | All API route definitions |
| `object/init.go` | Application initialization + LLM provider seeding |
| `conf/app.conf` | Runtime configuration |
| `web/src/App.js` | Frontend root component |
| `web/src/backend/` | API client helpers |

## LLM serving path (OpenAI-compatible /v1) — READ THIS

The `ai` module is mounted by `hanzoai/cloud` (HIP-0106). In prod it runs as
`cloud-api` in `hanzo-k8s`, fronted by `gateway` (api.hanzo.ai). Both
**hanzo.chat** (LibreChat, `baseURL https://api.hanzo.ai/v1`) and **hanzo.app**
(build) consume this exact surface.

Request flow for a completion:
1. `gateway` (api.hanzo.ai) validates the IAM JWT, mints identity headers,
   forwards `/v1/chat/completions` → `cloud-api:8000`. `/v1/models` is also
   proxied here; admin endpoints (`/v1/get-providers`, `/v1/update-provider`,
   `/v1/*-model-route`) are NOT gateway-exposed (use the direct
   `api.cloud.hanzo.ai` ingress + a session).
2. `controllers.ApiController.ListModels` (`/v1/models`) **requires a valid
   Bearer token** — unauthenticated returns 401 with no `data` array (this looks
   like "0 models"; it is not — authenticate to see the real list).
3. **Model → provider routing**: `resolveModelRouteForOrg` resolves in order
   DB routes (`/v1/*-model-route`, per-org → global "admin") → YAML config
   (`conf/models.yaml`, the `cloud-api-models` ConfigMap — **runtime source of
   truth in prod**) → static `controllers/model_routes.go` map. The YAML wins,
   so changing routing live = edit the ConfigMap (then `/v1/reload-model-config`
   or restart). The static map is the fallback when no YAML is present.
4. **Provider records** live in the DB (`object/init.go` `initLLMProviders`),
   keyed by name: `do-ai` (DigitalOcean GenAI, the primary — backs OpenAI/
   Anthropic/Llama/DeepSeek/Qwen/GLM/Kimi via `inference.do-ai.run`),
   `fireworks`, `openai-direct`, `zen`. Each call does
   `object.GetModelProviderByName(route.providerName)` → reads `ClientSecret`.
5. **Provider keys**: `ClientSecret` is `kms://SECRET_NAME`. `ResolveProviderSecret`
   (`object/kms.go`) resolves it **env-var-first** (`os.Getenv(SECRET_NAME)`),
   falling back to a real KMS call — BUT only runs when `kms != nil`, which needs
   `KMS_CLIENT_ID` or `KMS_SERVICE_TOKEN` set. So the working prod recipe is:
   set `KMS_CLIENT_ID` (flips kms on) + provide `DO_AI_API_KEY`/`OPENAI_API_KEY`/
   `ANTHROPIC_API_KEY`/`FIREWORKS_API_KEY` as env (from a K8s secret sourced from
   KMS-managed values). The env-first path means no live KMS dependency on the
   hot path.
6. **Provider re-seed self-heals** `ClientSecret`/`ProviderUrl`/`State`/`Type`/
   `SubType` on every boot from the `initLLMProviders` table — so fixing a stale
   key or upstream URL = edit the seed table and restart (no manual DB edit).

**zen models**: branded, `owned_by: hanzo`, `premium: true`. Route to `do-ai`
upstreams (qwen3+/glm/kimi/deepseek — same working key, no GPU). Identity is
injected at call time via `zenIdentityPrompt`; upstream names are never exposed.
Do NOT route zen to Fireworks serverless paths (`accounts/fireworks/models/*`) —
that account returns 404 "not deployed" for most of them.

**Billing gate** (`routers/filter_balance.go` + `openai_api.go`): non-premium
models need balance > 0; premium models need balance > starter credit. Balance
comes from `commerce` at `/v1/billing/balance?user=<org>&currency=usd`. Users in
`BALANCE_EXEMPT_USERS` (e.g. `hanzo/z`) bypass ALL gates — use the
`z@hanzo.ai` token to verify premium/zen without a credited org.

## Security invariants (READ before touching auth/billing/scaling)

- **JWT iss/aud** — every request-auth JWT path goes through
  `object.ParseAndValidateJWT` (signature via IAM **and** iss/aud policy), never
  raw `iam.ParseJwtToken`. `object/jwt_validate.go` sources the issuer from
  `JWT_ISSUER`/`IAM_ISSUER`/`CLOUD_IAM_ISSUER`/`AUTH_ISSUER` (any, default
  `https://hanzo.id`) and the audience allowlist from `GATEWAY_ALLOWED_AUDIENCES`
  (+ folds in `IAM_AUDIENCE`/`AUTH_AUDIENCE`), mirroring the gateway. A
  foreign-aud token → 401. The OAuth code-exchange callback (`account.go`
  `Signin`) is NOT a request-auth path and is intentionally exempt.

- **Balance ledger — single-pod invariant** — `object.GlobalBalanceLedger` is
  in-pod memory. Reserve/Settle are correct ONLY at `replicas: 1`. Two pods
  double-spend (each reserves against its own cached Commerce balance). Enforced
  by deploy `strategy: Recreate` + HPA `min=max=1` AND a boot assertion
  (`bootstrap.go` panics if `CLOUD_API_REPLICAS > 1`). To scale out you MUST first
  move Reserve/Settle behind a Commerce-atomic conditional reserve.

- **Single-request reservation** (`controllers/billing_reserve.go`) — an uncapped
  `max_tokens` is clamped (`clampMaxTokens`) and the reservation covers the larger
  of the clamped ceiling and the QueryText pipeline's fixed completion cap
  (`reserveCompletionTokens` / `reserveCompletionFloor=4096`, mirrored in
  `model/openai_util.go`), so actual spend can never exceed the hold on any path.

- **BALANCE_EXEMPT_USERS** — matched by the ONE shared `object.BalanceExemptSet`
  (gate + controller): an `owner/name` entry exempts only that exact subject; a
  bare `owner` exempts the whole org. `hanzo/z` does NOT exempt all of org hanzo.

- **Global admin** — `util.IsGlobalAdmin` = `IsAdmin && owner ∈ globalAdminOrgs`.
  Canonical default `defaultGlobalAdminOrgs = {admin, built-in}` (matches IAM
  AdminOrg + console2). The live `globalAdminOrgs` env override is authoritative;
  it is currently `admin,hanzo` (grants the hanzo TENANT org platform power) —
  FLAGGED for alignment to `admin,built-in` once the hanzo-org provider-config
  workflow is confirmed to run via the `admin` org.

- **Authz gate** (`routers/authz_filter.go`) — platform-sensitive endpoints
  (`globalAdminEndpoints`) are gated FIRST (before preview-mode + exempt reads).
  There is deliberately NO `adminDomain` Host bypass (a removed full-authz-bypass
  primitive). No principal → 401; wrong principal → 403.

- **User redaction** (`object/redact.go`) — `RedactUserSecrets` is an allowlist
  projection over string/[]string fields (`exposableUserFields`): a field not on
  the list is zeroed, so a NEW upstream secret field is fail-secure. Credential
  struct slices (MfaAccounts/ManagedAccounts/MfaItems/FaceIds) are nilled.
