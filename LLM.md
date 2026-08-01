# ai — agent guide

`hanzoai/ai` is the canonical **AI control plane** for the Hanzo platform: model
hub, native Go model routing, RAG, and MCP/A2A management. It speaks the
OpenAI-compatible `/v1` API, routes 66+ models to upstream providers, and meters
every request. Renamed from `hanzoai/cloud` (HIP-0106); mounts as the `ai`
subsystem inside `hanzoai/cloud`. In prod it runs as `cloud-api` on `hanzo-k8s`,
fronted by `hanzoai/gateway` at `api.hanzo.ai`.

**Canonical role.** This is a Hanzo *service/infra* repo — one impl, one place.
It is NOT an SDK; SDKs link out to it. Completeness order across languages is
Python → Rust → C++ → Go. Canonical spec: `~/work/hanzo/SDK-ARCHITECTURE.md`.

**Brand rules (hard — enforce in every edit).**
- Never call this an "LLM gateway" and never position it against LiteLLM — it is
  a full AI cloud / control plane, not a proxy.
- `/v1/` only, never an `/api/` prefix.
- Zen models are Hanzo's own family (`owned_by: hanzo`) — never name upstream models.
- Voice: "Hanzo — the Open AI Cloud." Modern, crisp, developer-first.

**Install / run.**
```bash
go build -race -ldflags "-extldflags '-static'"   # build
./cloud-api-server                                 # run (env-configured)
go test -v $(go list ./...) -tags skipCi           # test (requires MySQL)
docker compose up                                  # local stack
```

**Key entry points.** `main.go` (entry) · `bootstrap.go` (boot + replica
assertion) · `routers/router.go` (all `/v1` routes) · `controllers/` (HTTP
handlers) · `object/init.go` (init + LLM provider seeding) · `model/` (provider
integrations) · `object/kms.go` (secret resolution) · `web/` (React admin UI).

## Architecture

Full-stack application:
- **Backend:** Go 1.26 + native web router (`github.com/hanzoai/ai/web`, served as `routers.App`; upstream beego dropped), MySQL/MariaDB/PostgreSQL
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
- Framework: native web router (`github.com/hanzoai/ai/web`, served as `routers.App`) — controllers handle HTTP, objects contain business logic; upstream beego is dropped (no direct import — routing, config, logging, pagination are all native)
- New AI providers go in `model/` (implement the provider interface)
- Database access via the native store in `object/`
- Route registration: CRUD resources are GENERATED from the one table in `routers/resources.go`; `routers/router.go` holds only what is NOT a resource (the OpenAI-compatible surface and a few singletons). See "The /v1 resource surface" below.
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

- Backend config: `conf/app.conf` read by the native `conf.AppConfig` ini reader (env-first; absent in the embedded runtime, which runs env-only)
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

## The /v1 resource surface — ONE table, no compound routes

The published OpenAPI description is GENERATED from that same table:

    go run ./cmd/openapi -spec ../openapi/ai/openapi.yaml

It writes only the region between the `# BEGIN/END generated` markers in the
canonical spec (hanzoai/openapi, OpenAPI 3.1) — the inference paths above it are
hand-authored with real OpenAI request/response schemas and stay that way.
`cmd/openapi -verify` and `TestSpecMatchesTheTable` fail when the spec drifts from
the table, so the contract customers and SDKs read cannot describe a surface this
service does not serve.

The old `swagger/` directory is DELETED. It was a second description, and it was
wrong: `basePath: /api` with 135 `/<verb>-<noun>` paths, a base path this service
has never served. Nothing referenced it, nothing embedded it, and no test noticed
when it stopped being true — which is the argument for generating the replacement
rather than hand-maintaining it.

Every CRUD route is generated from `routers/resources.go`. There is no second
registration path, and no route is written by hand.

    GET    /v1/<ns>/<resource>                 list      POST   /v1/<ns>/<resource>          create
    GET    /v1/<ns>/<resource>/<owner>/<name>  read      PATCH  …/<owner>/<name>            update
                                                         DELETE …/<owner>/<name>            delete
    GET    /v1/<ns>/<resource>/global          cross-tenant list (where a resource has one)

Namespaces are the subsystems that own the data, so a reader can tell what a route
touches from its prefix: `auth` (this backend's own account/session objects),
`rag`, `chat`, `ai`, `content`, `compute`, `work`, `ops`.

Three things about this that are easy to get wrong:

**The member key is `<owner>/<name>` — two path segments, not one.** Every object
is keyed by that pair (`util.GetOwnerAndNameFromIdWithError` requires EXACTLY two
tokens, so a name containing a slash was never valid — the two-segment URL is
lossless, not a narrowing). They are recomposed into `id` in the one place route
params are bound (`web.Router.ServeHTTP`), so handlers keep reading
`c.Input().Get("id")` unchanged. A single `:id` segment cannot work: Go decodes
`%2F` back into a separator before routing.

**`/v1/iam` belongs to the IAM service, not to this one.** cloud proxies that whole
subtree away (`cloud/iam_edge.go`). Anything registered there is shadowed and
unreachable — it fails as a wrong answer, not an error.
`TestNoResourceClaimsForeignNamespace` holds that line.

**The policy filters key on names, not paths.** `authz_filter` and `filter_balance`
hold hand-curated sets (super-admin endpoints, balance exemptions, demo-mode
allowances). Those names are DERIVED from the same table by `policyKey`, never
restated — so a policy entry cannot drift from the route it governs. Do not
hand-edit a path into those maps.

Adding a resource is one table entry. The tests will tell you if you got it wrong:
every controller method the table names is checked to exist by reflection, every
route must emit a policy key, no path may contain a verb, and an action whose
handler reads no id must sit on the collection (a member action's route could never
match).

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

**Async video (`/v1/videos`) — Sora-style, NOT sync.** Text-to-video generation
takes minutes, so `/v1/videos/generations` is ASYNC (the sync handler used to
hold the request ~104s → console `/ai` proxy timed out → browser 502 while the
generation actually succeeded). The lifecycle is three fast requests:
`POST /v1/videos/generations` (create → returns a `video_<uuid>` job id +
`queued` object immediately), `GET /v1/videos/{id}` (one status poll), and
`GET /v1/videos/{id}/content` (stream the finished MP4). `zen3-video*` route to
the **spark-video** provider (our GB10, `spark-video.hanzo.ai/v1`), whose OWN
`/v1/videos` API is already the same async create→poll→download shape the three
`model.{Create,Retrieve,Download}VideoDOAI` primitives proxy. Metering is per-user
and lands EXACTLY ONCE on completion: create RESERVES the per-video budget and
carries the `*budgetHold` in the in-pod `videoJobStore` (valid — the pod is
single-replica, the balance ledger's own invariant); the first request that
observes `completed` (poll or download) settles the hold + records the single
Commerce usage event (`videoJob.markCompleted`, idempotent); a failed job releases
the hold and bills nothing; a reaper releases abandoned/wedged jobs' holds (TTL)
so budget never leaks. Every poll/download re-runs the shared auth+routing policy
and enforces OWNERSHIP (caller subject == job subject, else 404 — never confirm
another user's job); the provider key is re-resolved per request, never stored.
The `gateway` passes every `/v1/*` sub-path through to cloud-api, so the poll/
content routes need no gateway change; the console `/ai` proxy allow-list adds the
`v1/videos/{id}[/content]` pattern (hanzoai/console#92).

**zen models**: branded, `owned_by: hanzo`, `premium: true`. Route to `do-ai`
upstreams (qwen3+/glm/kimi/deepseek — same working key, no GPU). Each zen model
declares its public owner in `owned_by`; the identity is injected at call time
via `zenIdentityPrompt` (hip-00NN). Do NOT route zen to Fireworks serverless
paths (`accounts/fireworks/models/*`) — that account returns 404 "not deployed"
for most of them.

**Billing gate** (`routers/filter_balance.go` + `openai_api.go`): non-premium
models need balance > 0; premium models need balance > starter credit. Balance
comes from `commerce` at `/v1/billing/balance?user=<org>&currency=usd`. Users in
`BALANCE_EXEMPT_USERS` (e.g. `hanzo/z`) bypass ALL gates — use the
`z@hanzo.ai` token to verify premium/zen without a credited org.

## AI Login Manager — universal metering + connected accounts + 1% BYO fee

`ai` is the centralized login manager for every user's AI providers. Every AI call
from every surface (@hanzo/dev, Hanzo Desktop, hanzo.chat, hanzo.app) routes through
THIS router at `api.hanzo.ai/v1`. For the caller's org the router resolves WHICH
account executes the call, runs it, and meters ONE usage event — regardless of whose
credits paid for it. There is exactly one meter and one credential store.

**Two account modes (one router, one meter):**
- **Hanzo credits (default).** No org-owned provider for the requested model →
  the global built-in (`Owner == "admin"`) serves it. Billed = token list-price
  cost (existing behavior). No surcharge.
- **Connected third-party account (BYO).** The org connected its OWN OpenAI /
  Anthropic / Google account via `/v1/ai/connections`. The router uses the org's
  KMS-sealed key, the customer pays that provider directly, and Hanzo bills a **1%
  platform fee** on the provider list price for routing + metering + management.

**BYO is the EXISTING BYOK seam, not new plumbing.** `resolveProviderForUser` →
`object.GetModelProviderByNameForOrg(org, name)` returns the org's own provider row
when it has one (`Owner == org`), else the global `admin` built-in. `providerBYO`
(`controllers/platform_fee.go`) classifies the resolved pair: BYO iff
`provider.Owner != "" && != "admin" && == user.Owner`.

**Metering — one path, extended not rebuilt.** Every completion terminal already
calls `recordUsage` (→ commerce `POST /v1/billing/usage`) + `recordTrace` (→ the
`hanzo.cloud_usage` warehouse). This feature adds three dimensions to that ONE
event: `byo`, `fee_cents`, `account`. `platformFeeCents(costCents, byo) =
byo ? ceil(costCents/100) : 0`. Billed `amount = byo ? fee_cents : cost_cents`; the
full `cost_cents` is recorded on every row for analytics even when only the fee is
billed. `hanzo.cloud_usage` is the ONE global usage store (its `organization` column
is the row-level tenant filter — a tenant reads only its own org; the admin org
reads unfiltered). The rich per-org analytics lens is entitlement-gated in
`hanzoai/cloud` (`analytics.datastore`); basic own-org usage is always readable.

**Connected accounts API (`/v1/ai/connections`, IAM-authed, org-scoped).** Curated
login-manager surface over the per-org `object.Provider` store
(`controllers/connections_api.go`). Keys are sealed via `object.StoreProviderSecret`
→ `kms://…` ref (fails closed if KMS is down); the raw key is NEVER stored in a row,
returned, or logged.
- `GET /v1/ai/connections` → `[{provider, connected, account_label, updated_at}]` (never the key)
- `POST /v1/ai/connections` `{provider, apiKey}` → seals key, activates the org row; returns metadata only
- `DELETE /v1/ai/connections/:provider` → deactivates the org row (resolution reverts to Hanzo credits)

**Enforce "even if logged in".** All AI traffic must reach this router; no surface
bills a third party off-meter. hanzo.app's workspace default was flipped
`openrouter → hanzo`; hanzo.chat routes via its gateway config; @hanzo/dev defaults
to `api.hanzo.ai/v1` under Hanzo login. Claude/Opus for @hanzo/dev is served HERE
(Anthropic-native `/v1/messages` + OpenAI-compat `/v1/chat/completions`) against the
org's connected Anthropic account + 1% fee — no direct-to-provider bypass.

**Next slice (route selection).** Connections are stored/sealed/listed and the
1%-fee metering fires the instant the resolver returns an org-owned provider. But
the route table maps all models to `providerName: "do-ai"` (the aggregating
upstream), so a connection row named `openai`/`anthropic`/`google` is not yet
SELECTED for its model family. Making a connected consumer account actually serve
its models is a bounded route-table change (model-family → org-connection selection
+ real upstream model-id/base-url mapping) — deliberately not in this slice.

## RAG — ONE unified surface (retires the standalone chat-rag-api)

`ai` is the single RAG layer for the whole platform. There is exactly ONE
retrieval pipeline; the standalone `hanzoai/chat-rag-api` (danny-avila/rag_api
fork) is now redundant and slated for archive.

**One index, one embedder, one retrieval path.** Every document — doc/crawl/
github/s3 ingest AND per-file uploads from hanzo.chat — lands in the SAME
per-tenant index `{owner}-{store}-docs`, fanned out to BOTH Hanzo Search
(keyword, Meilisearch) and Hanzo Vector (semantic, Qdrant) via
`object.IndexDocuments`, and retrieved via `object.SearchDocuments` (hybrid RRF).
See `object/search_docs.go`.

**File-scoped RAG is a FILTER, not a parallel store.** `file_id` is a filterable
dimension on that one index (`DocIndex.FileID`; Meili filterable attr `file_id`;
Qdrant payload `file_id`). Uploaded-file retrieval = a `file_id` filter over the
tenant index. `object/rag.go` holds the file-scoped logic (`RagEmbedFile`,
`RagQuery`, `DeleteRagFile`, `RagFileContext`); chunking uses
`split.RecursiveSplitProvider` (parity with rag-api's
`RecursiveCharacterTextSplitter`, 1500/100), parsing uses
`txt.GetParsedTextFromUrl` (PDF/CSV/XLSX/PPTX/…). Uploaded files default to the
`rag-files` store so they don't pollute curated docs.

**Two HTTP projections over the one logic** (`controllers/rag.go`,
`controllers/rag_librechat.go`):
- Native (canonical): `POST /v1/rag/embed`, `/v1/rag/query`,
  `/v1/rag/query-multiple`, `/v1/rag/delete`, `GET /v1/rag/context`.
- LibreChat-compat (drop-in for the retired rag-api's fixed contract):
  `POST /v1/embed` (multipart), `/v1/query`, `/v1/query_multiple`,
  `DELETE /v1/documents`, `GET /v1/documents/:file_id/context`. Returns the
  LangChain `[[{page_content,metadata},score],…]` tuple shape hanzo.chat parses.

**Migration**: point hanzo.chat's `RAG_API_URL` at `https://api.hanzo.ai/v1` —
no chat-repo change. Auth/billing reuse the doc-RAG path
(`resolveSearchAuth`/`requireIndexAuth` + `recordSearchUsage`); owner is the
authenticated principal (tenant isolation), never a client-supplied field.

## Search / Crawl / Web-search — THREE orthogonal products, ONE way each

Three distinct products, no overlap. Do NOT add a fourth crawl path.

- **`POST /v1/search` = native full-text index search** (`SearchDocs` →
  `object.SearchDocuments`). Hanzo Search (Meilisearch, keyword, `hanzoai/search-go`
  at `searchHost`:7700) + Hanzo Vector (Qdrant semantic, `vectorHost`:6333) fused
  hybrid-RRF over the per-tenant index `{owner}-{store}-docs`. Returns `{hits:[…]}`.
  Needs a populated index — an empty/new tenant index returns `{hits:[]}` (no error).
  `GET /v1/search/stats` reports index size.

- **`POST /v1/crawl` = crawl a URL, get content back** — THE single canonical
  crawl (`controllers/crawl.go` `Crawl` → `object.Crawl` → `CrawlWithCrawl4AI`).
  Backed by the self-hosted **Crawl4AI** (`hanzoai/crawl`, `crawl.hanzo.svc`:11235,
  Apache-2.0). Body `{url}` or `{urls}` (merged, deduped, capped at 10); returns
  `{results:[{url,title,description,markdown,success,metadata}]}` with LLM-ready
  markdown (prefers Crawl4AI `fit_markdown`, falls back to `raw_markdown`).
  Auth: `requireIndexAuth` (fail-closed, rejects read-only pk-/hz_ keys) + balance
  gate; usage recorded via `recordSearchUsage(…, "crawl", "crawl4ai", …)`.
  `object/crawl4ai.go` handles the deployed **Crawl4AI 0.8.6** wire shapes:
  `markdown` is an OBJECT (`MarkdownField` polymorphic decode), the `/crawl`
  envelope is synchronous with a boolean `success` and no `status`/`task_id`
  (return inline `Results` whenever present), and `links`/`media` values are
  heterogeneous (`map[string][]map[string]interface{}`).

- **`POST /v1/scrape` = crawl-and-INDEX (ingest)** — NOT a general crawl. It
  crawls a site and WRITES structured content into the tenant search index,
  returning `ScrapeStats` (`ScrapeDocs` → `object.ScrapeAndIndex`). Sibling of
  `POST /v1/index` (index caller-supplied docs). Stays because its output/effect
  (index write) is distinct from `/v1/crawl` (fetch + return).

- **`POST /v1/scrape/preview`** is a DEPRECATED alias of `/v1/crawl` — its handler
  now forwards to `Crawl` (one implementation, no parallel path). Use `/v1/crawl`.

- **`/v1/websearch/*` (lives in `hanzoai/cloud`, not ai) = web meta-search** — a
  separate product: SearXNG-compat `/v1/websearch/search` + a firecrawl-compat
  `/v1/websearch/v1/scrape` leg that hanzo.chat's web-search plugin calls. That
  leg is a fixed EXTERNAL contract (firecrawl `{success,data:{markdown}}`) and
  reuses the SAME Crawl4AI backend — it is an integration shim for the web-search
  flow, not a general crawl endpoint. The general "crawl a URL" is ONLY `/v1/crawl`.

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

## OPEN DEFECT — recorded COGS is retail, so margin is structurally 0

**Not fixed. Do not "fix" it by editing the enso rates; the rates are correct.**

Every `hanzo.cloud_usage` row for the Enso family (1,296 rows checked, ClickHouse
at `datastore.hanzo.svc`) has `billed_nano == cost_nano` and `margin_nano == 0`.
The recorded cost is RETAIL, not what the upstream actually cost us.

The mechanism is a deliberate fallback used outside the case it was written for:

- `controllers/model_pricing.go:43-57` — `costInputPerMillion()` /
  `costOutputPerMillion()` return `CostInPerMillion` / `CostOutPerMillion` when
  set, **else the customer price**.
- `controllers/billing_nano.go:75-79` — `tokenProviderCostNano` computes COGS at
  exactly those rates, so a model with no registered COGS reports cost == billed.
- `controllers/billing_nano.go:124-135` — `providerCostNano` feeds that into
  `costMargin`, which the ledger, the `cloud_usage` row, and the o11y span all
  read.

For Enso no COGS rate is registered, so the fallback fires on every call. The
real COGS is known and lives in the Enso catalog: `1.392 / 2.784` $/MTok on the
`deepseek-v4-pro` anchor (enso and enso-ultra) and `0.112 / 0.224` on
`deepseek-4-flash`. Against billed 4/20 the true margin is roughly 2.9x in /
7.2x out — recorded as zero.

A channel to carry the exact figure already exists and is simply not populated:
`controllers/trace_export.go:40-41` takes `BilledNano` / `CostNano` with
`0 = recompute from rates`, surfaced as `CostNanoExact` in
`controllers/openai_api.go:557-558`. The serving side knows its per-arm cost —
for enso-ultra the fan-out means COGS is the real sum of the arms that ran, which
only the server can total — so pushing the exact value is the right seam. Falling
back to rate math cannot express a fan-out cost at all.

Consequences, in order of severity:

1. **The admin cockpit's below-cost check cannot work for these models.** It
   compares price against a "cost" that IS the price, so the difference is
   always exactly zero: it can neither fire on a genuine below-cost sale nor
   stay quiet honestly. This is the likely source of its misfiring.
2. Margin reporting reads 0 for the whole family, so gross margin is understated
   wherever it aggregates `margin_nano`.
3. The zero is indistinguishable from a real zero-margin sale (BYO calls
   legitimately record `CostNano = 0`), so nothing downstream can tell "we did
   not compute this" from "this genuinely earned nothing".

The fallback is defensible as a default — a new model with no COGS should not
report imaginary profit — but it is silent, and silence is what let it stand.
Whatever the fix, an unregistered COGS should be *distinguishable* from a
measured one rather than quietly equal to price.
