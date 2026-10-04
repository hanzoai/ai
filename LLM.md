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
- **Backend:** Go 1.26 on `github.com/zap-proto/zip` (over fiber v3 / fasthttp), MySQL/MariaDB/PostgreSQL
- **Frontend:** React + Ant Design v5, located in `web/` (the folder is the React app; the in-house Go router that used to share the name is gone)
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
- Framework: `zip`. An `ApiController` IS its request — it embeds `*zip.Ctx`, so there is no context to marry to a controller and no recorder to read afterwards. Controllers handle HTTP, objects hold the business logic.
- New AI providers go in `model/` (implement the provider interface)
- Database access via the native store in `object/`
- Route registration: CRUD resources are GENERATED from the one table in `routers/resources.go`; `routers/router.go` holds only what is NOT a resource (the OpenAI-compatible surface and a few singletons). See "The /v1 resource surface" below.
- i18n strings: avoid duplicate keys across frontend and backend

#### Five things zip decides that the old router did not

Each cost a real defect during the migration, and each is invisible: the code
compiles, the types check, and the behaviour is wrong.

- **A WRITE CARRIES ITS STATUS.** `c.JSON(code, v)` / `c.Bytes(code, b)` set the
  status as they write, so setting one beforehand and writing after replaces it.
  `c.Status` is for a response whose body is written by something that takes no
  status — a stream — or that has no body at all. Everything else passes the status
  to the one write. A refusal that arrives as 200 is worse than an outage: the
  client reads it and believes it.

- **A STREAMED BODY IS PRODUCED AFTER THE HANDLER RETURNS.** fasthttp drains the
  writer handed to `SendStreamWriter` while it serialises the response, so the
  callback has NOT run when the call comes back — measured, not assumed. Anything
  the stream reads must outlive the handler (hand the upstream body to the
  callback; a `defer` in the enclosing function closes it first and the client gets
  nothing), and anything the stream learns must be used inside it (token counts read
  outside are still zero, so a streamed answer bills no completion).

  **AND THE CTX IN THERE IS NOT THE REQUEST'S — this line used to say it was, and
  production refuted it.** The claim was that fasthttp finishes writing before
  fiber releases the request, so reading `c` from the callback was safe. Measured
  on api.hanzo.ai: `DefaultCtx.App()` nil-dereferenced under
  `recordFamilyUsage -> billingOrg -> Ctx.Header`, twelve times an hour. It does
  not fail the one request either — a SIGSEGV takes the whole plugin process, so
  every other call in flight dies with it and each one answers
  `mount /v1: http: read response: EOF`. Read what the stream needs from the
  request BEFORE the handler returns and let it travel by value; `whence` in
  `zen_client.go` is that shape, and `recordFamilyUsage` takes no receiver so
  there is nothing to reach through.

- **`RequestURI` IS SET BY A SERVER, NOT BY A CALLER.** The fiber adaptor reads that
  field, so a request CONSTRUCTED in process — which is every request crossing ZAP —
  arrives at the router as `/` and 404s. `controllers.target` wraps a handler to
  fill it in from the URL; both ZAP installers go through it.

- **A BUFFERED WRITE IS NOT A DELIVERY.** Bytes leave at the flush. A `Write` that
  returns n > 0 has reached a buffer, and an unchecked `Flush` reports a broken
  connection as success — which is how `AnthropicWriter.StreamSent` came to tell the
  failover loop an answer had reached a client it never reached.

- **A PRINCIPAL IS A VERIFIED TOKEN, and there is nothing else to seed.** No session
  store exists; `GetSessionClaims` parses the credential the request presents. So a
  test that needs a caller PRESENTS one: `presenting(visit(method, target),
  authtest.Bearer(t, user))`. `internal/authtest` is the ONE installer of the
  verifier's certificate — two of them race over the same global. Corollary: any
  predicate that used to mean "there is a session" now fires for ANY verified token,
  a sibling brand's included.

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

## hedge — ask several providers, keep the first answer

`hedge.Race(ctx, dst, attempts...)` is the primitive behind fast mode. Tail
latency is not the average: a provider that usually answers in 400ms occasionally
takes eight seconds, and a request cannot know which it drew until it has already
waited. Asking three and keeping the first cuts that tail.

**No provider changes, and none are needed.** Every provider already writes into
an `io.Writer`, so `Race` hands each attempt a different one and only lets the
winner's reach the caller. `ModelProvider` is untouched — the seam was already
the right shape.

**FIRST BYTE WINS**, the only definition of fastest a stream can act on. A
provider that will finish sooner but has not started is not the winner: the
caller waits on bytes, not completion, and by the time completion is knowable the
response has arrived and there was nothing to race.

Two cases decide whether hedging helps or hurts, and both are pinned by tests
verified against a deliberately broken implementation:

- An EMPTY write does not claim. A provider flushing a zero-length chunk while
  still waiting upstream would otherwise win having said nothing, leaving the
  caller on the SLOWEST attempt with the others cancelled — the opposite of the
  point.
- A FAST FAILURE is not a win. `Race` returns when the attempt that CLAIMED the
  stream finishes, not when the first attempt returns; an upstream 503 in 1ms
  produced no bytes, so the caller is still waiting on whoever answers.

Losers are cancelled — that is what stops the upstream call, which is what stops
the cost — and `Race` does not wait for them to unwind, because waiting on a
provider we are abandoning hands it exactly the latency this removes.

### One ledger path each, because the two losers are opposite cases

`ask.fan` (`controllers/hedged.go`) carries a request into fast mode: how many to
ask, where the winner's bytes go, and how to make and adopt a writer. `serve()`
dispatches on it, so a caller sets a NUMBER rather than choosing a code path, and
a fan too narrow to race falls back to the cascade with its retry and demotion
intact. **Nothing sets it yet** — the mechanism ships inert, and
`TestWithoutAFanNothingChanges` is what says so.

A raced loser and a failover refusal wear the same shape and mean the opposite
thing, which is the whole design:

- A REFUSAL errored. No tokens, no upstream charge — `recordRefusals` writes a
  `"failover"` row that bills nothing, and that is correct.
- A RACED LOSER was CANCELLED mid-generation. The vendor produced tokens and
  charged us. Filing it as a refusal records a real charge as free and makes fast
  mode invisible in the ledger the team reads.

So a beaten provider never appears in the attempts `race()` returns — the caller
records those as refusals — and settles through `fan.bill` instead.

**Each attempt needs its OWN writer.** The loser's token count is on ITS writer,
and one writer shared across attempts holds exactly one answer: the winner's. A
shared writer therefore bills every loser for the winner's tokens AND glues its
half-sentence to the winner's answer on the client's wire — both failures show up
in `TestEachRacerIsBilledFromItsOwnWriter`, verified against a deliberately
shared fork. `OpenAIWriter.fork`/`adopt` are that pair: forks copy the dialect
and start the buffers empty; the winner's is adopted so the non-streaming body
and the tail chunk come from the provider that actually served.

**THE BILL ARRIVES LATE, and that is the shape rather than a wrinkle.** `Race`
returns when the winner finishes, not when the losers do — so at that moment a
loser has typically not even started, and reading its outcome is both a missing
bill and a data race. (The first draft did exactly this; the test caught it.)
`settle` therefore runs on its own goroutine, and `fan.bill` must close over
VALUES: by the time it fires the handler has returned and, on fasthttp, the
request has been recycled.

**Cancellation is a WRITE ERROR, not a context.** `QueryText` takes no context,
so a loser's only stop signal is `hedge.ErrLost` on its next write. One that
checks its write error stops there; one that does not runs to completion and
bills us for all of it. Either way the tokens are real — which is the argument
for billing them.

### The switch, and why it is decided with the reservation

A request asks with `"fast": true` in the body, or `X-Fast: 1` for callers with
no browser in the way — the body form exists because a page cannot send a custom
header past the edge's CORS allow-list, exactly as `retrieval` works
(`controllers/fast.go`).

**What a race costs is settled WITH the hold, never after.** `widthFor` reserves
`est × fastWidth` before anything is spent, because N attempts is N completions
and a hold sized for one is a promise the ledger cannot keep — the losers' rows
land later and take the balance past the point the gate said it would stop.
`fastWidth` is 2: the tail this cuts is one provider drawing a slow response
while another would have answered normally, and a second opinion removes most of
it for one extra completion rather than two.

Two refusals that are deliberately NOT refusals:

- **Cannot afford the race** → serve the ordinary request. The caller asked to go
  faster, not to be turned away, and a new way for a funded account to be told no
  is worse than quietly being normal speed.
- **Nobody to bill** → never race. The public lane makes no reservation, so
  hedging there spends N completions on a stranger and settles none.

Still open: the toggle in the products. hanzo.chat follows `web_search`;
hanzo.app follows `base` and must resolve its in-flight merge on the three
AI-path files first. The API accepts the field today, so either can ship the
control without waiting on the gateway.

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

The namespace is `ai` for every row, and for every other route this service
serves. HIP-0139 §3.1: a capability answers at ONE address, `/v1/<name>`, and a
second top-level address is a second capability or a misfiled route — never an
alias. So `/v1/router/*`, `/v1/org/settings`, `/v1/finetune/*`, `/v1/memory/*`,
`/v1/rag/*`, `/v1/docs/ingest`, `/v1/feedback` and `/v1/traffic/globe` are all
under `/v1/ai/…` now (ingest under `/v1/ai/rag/`, since it writes the index rag
reads). The groupings in the table — retrieval, chat, models, content, compute,
work, ops — are how it is READ, not a second prefix.

Two families stay outside `/v1/ai` because a protocol, not this repo, fixes
their spelling (§3.2): the OpenAI/Anthropic wire (`/v1/chat/completions`,
`/v1/completions`, `/v1/embeddings`, `/v1/messages`, `/v1/models`,
`/v1/images/*`, `/v1/audio/*`, `/v1/videos/*`, `/v1/responses`, `/v1/rerank`),
and `POST /v1/embed`, the multipart form hanzo.chat posts.

**`/v1/ai/<noun>` is a resource collection address.** Nothing may register a ZAP
gateway PREFIX there: the registry matches on path alone, so a prefix at a
collection swallows that collection's `:owner/:name` members and answers with
the LIST handler — a wrong answer, not an error. Deeper addresses
(`/v1/ai/rag/query`, `/v1/ai/router/defaults`) are routes, not collections, and
are safe. `TestNoGatewayPrefixOverTheResourceNamespace` holds the line; it is
why `/v1/ai/feedback` has no gateway prefix and reaches routers.App through the
fallback instead.

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

## An OpenAI-COMPATIBLE vendor is upstream `Local`, never `OpenAI`

`Provider.Type` IS the dialect declaration, and the two OpenAI-shaped upstreams are
not interchangeable:

| type | client | dialect | price from |
|---|---|---|---|
| `OpenAI` | `NewOpenAiModelProvider` | **`/v1/responses`** — OpenAI's own surface | `CalculateOpenAIModelPrice`, a table of what OpenAI RETAILS |
| `Local` | `NewLocalModelProvider` | **`/v1/chat/completions`** (`local.go`, `CreateChatCompletionStream`) | the provider ROW (`inputPricePerThousandTokens`), plus `compatibleProvider` for the API it speaks |

`DigitalOcean` already resolves to the `Local` client; so should every vendor that
merely SPEAKS OpenAI. Typing one `OpenAI` sends it to the Responses API, and every
compatible vendor shims that differently.

**Measured, one provider (the Hugging Face router), one credential, one URL —
typed `OpenAI`:** `meta-llama/Llama-3.3-70B-Instruct` answered, while
`zai-org/GLM-5.2`, `deepseek-ai/DeepSeek-V4-Flash-0731` and `moonshotai/Kimi-K3`
returned an EMPTY message with a 200 and zero usage. All four answer directly, in
both dialects. **Retyped `Local`, all four answer through the plane**, reasoning
models included, with no `<think>` leaking into content — and no code change.
The reasoning streams carry `response.reasoning_text.delta`, which the Responses
loop does not case on (it handles only `response.reasoning_summary_text.delta`).

So the rule is a ROW, not a patch: an OpenAI-compatible vendor is `Local` with its
own URL and its own price. Reasoning stays a FIELD the dialect carries, never
something sniffed out of the content stream.

**Three things still dispatch on substrings of the model NAME**, and a model in
none of the lists gets wrong behaviour rather than an error — `getOpenAiModelType`
(which dialect), `InlinesReasoning` (`strings.Contains(name, "deepseek")`, directly
under a comment saying which SKU inlines is "a property of the catalog (HIP-0039)
… never a list here"), and the retail price table. Each is a place a new model
silently misbehaves; the `Local` row above is what they were compensating for.

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
   A models.yaml entry with `alias_of: <id>` is an alias: not a route, not
   listed. `AliasFilter` rewrites a POST body naming it to name `<id>` ahead of
   every other reader, so it is routed, gated and billed as `<id>`. An alias of a
   zen SKU belongs in the zen catalog instead: cloud's in-process zen claims
   the request before this chain runs. `/v1/decisions` sends the service the
   route's `upstream` and answers naming the id asked for.
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
comes from `commerce` at `/v1/billing/balance?user=<org>&currency=usd`. No
principal bypasses it: the gate always consults the balance, and a lookup that
fails returns 500 rather than free access. To verify premium/zen, credit the org.

## The chat relay — the caller's body, as written (`controllers/forward.go`)

Every non-family `/v1/chat/completions` (and `/v1/responses`, which becomes one)
whose route row `relays` — an OpenAI-compatible chat address, not Anthropic's
dialect — is sent the CALLER'S OWN BODY. Plain text, tools and images take the
one path. The relay writes four things and only these: `model` (each provider's
own id), the completion ceiling when the hold covers less than the caller asked
(under the caller's key; when they named none, `max_completion_tokens` to an
`OpenAI` row and `max_tokens` elsewhere), `stream_options.include_usage` on a
stream, and retrieved knowledge as one leading system message. `ours`
(`fast`/`retrieval`/`retrieval_store`) is read here and not sent on. `unpriced`
(`models`, `provider`, `transforms`, `route`, `plugins`, `web_search_options`,
`service_tier`: fields that buy something on OUR account the SKU price does not
cover) is REFUSED 400 by name on every path, before any vendor is asked, never
stripped. Nothing else is touched: every turn, temperature 0, seed,
`response_format`, `stop`, vendor extras. `chatRequest` holds `response_format` and `stop` raw because go-openai
cannot decode a `json_schema` or a string `stop`, which used to 400 before routing.

It fails over the way `ask.cascade` does — same `candidates`, `retryTransient`,
`cooled`, `announce`, `recordRefusals`, `exhausted` — and every decision is made
on the STATUS, before a byte is written. A stream is judged by its opening frames
(`opening`, the family's rule): an error inside a 200 moves the request; the
first answer frame commits it, and nothing after that moves it. `dial` is the
seam the cascade tests script. Billing is by the SKU, with
`prompt_tokens_details.cached_tokens` split out and priced as cached.

**A relayed stream OWNS ITS HOLD.** The callback settles the real cost after the
handler has returned, and `budgetHold.settle` is one-shot, so the handler's
deferred release would win and the local ledger would never see a stream's spend.
`forward` reports that it handed the hold to a stream and the handler lets go;
the callback releases it on the way out if it ends without settling. The text
pipeline and the family pipe still release before their streams settle.

**The relay does not race.** A relayed attempt is committed at its status, so a
`fast` request takes the cascade; the reservation `widthFor` made covers it.
Anthropic-type rows and endpoint-less rows keep the older paths
(`proxyToolRequestAnthropic`, the QueryText cascade). An Anthropic-type FALLBACK on
a relayed route is passed over, not converted.


## Strict mode — served unchanged or refused (`controllers/strict.go`)

`X-Hanzo-Strict: 1` on `/v1/chat/completions` asks for the request to reach the
route's FIRST row exactly as sent, or be refused 409 `strict_violation` with the
broken invariant in `X-Hanzo-Strict-Violation`: `route_auto` (auto-routing),
`translation` (a family pipe, an Anthropic/QueryText row, `/v1/responses`),
`ceiling_unset` (no max_tokens named: the relay would write one), `ceiling_lowered`,
`rag`, `param_dropped`, `rewritten`, `added`, `not_canonical` (a repeated name).
No fallback, no cooled reordering: a vendor refusal is the answer. Served, it
carries `X-Hanzo-Request-Sha256` and `X-Hanzo-Upstream-Sha256` — SHA-256 of the
RFC 8785 form (go-json-experiment `jsontext.Canonicalize`) of the client body and
the upstream body, each without `model` and `stream_options.include_usage`; they
are equal on every strict answer. `/v1/messages` answers strict requests in its
own file (anthropic_api.go).
## Decisions — `/v1/decisions`, the one decision path

`controllers/decisions.go`. It reaches the decision service (`KAI_URL`) through one
core, `decide`, which the HTTP handler and the ZAP twins (`zap_decisions.go`) call
after resolving the principal. It serves Kai (`kai`, `kai-<12 hex>`) and Jev by
OpenRouter's vendor ids (`typesafe/jev-1.13`, `~typesafe/jev-latest`), selected by
`model`; the vendor ids reach Jev itself. Nothing named Jev is ever answered by Kai
(`jevNamed`), and a bare `jev-*` has no route, so it is 400. Every refusal is
`{"error":{"code","message"}}`. Kai bills $0.021 and Jev its list $0.042 per
million input tokens — the same row in `model_pricing.go`,
`conf/models.yaml` and hanzoai/pricing's `decisionCatalog`.

- **One principal pays.** The ledger org is what the reservation, the debit and
  every handle name: `billingOrg` on HTTP; on ZAP the same rule over the gateway
  request's `X-Org-Id` (carried on ctx, `orgAsked`) and the JWT's signed `orgs`. An
  IAM or vendor key has no membership and pays from its own org.
- **A handle belongs to the org that observed it.** `observe`/`handle` are sent as
  `<org>/<id>`; `unscope` takes the prefix off anything said back. An answer with no
  handle in it goes back byte for byte.
- **One body bound, shared with the service.** `decisionBodyBytes` (16 MiB) is the
  decision service's `BODY_BYTES`; change both together. A body past it is 422
  `request_too_long`, after authentication. ai's socket admits
  more (26 MiB), so the handler sees it; a refusal a layer RETURNS (the framework
  reading an over-limit body included) is worded by `Dialect` via
  `controllers.Refusing`. A transport-level refusal (zip's raw fasthttp server,
  above the socket's own limit — or the cloud edge's `GATEWAY_BODY_LIMIT` in front
  of ai) is written by fasthttp before any handler and cannot be worded here.
- **The body is read key for key, and only after the credential** (`fieldsOf`): the
  size bound is the one thing asked before `vouched` accepts the credential; then an
  unknown top-level field (a case variant of a known one included) or a repeated one
  is 400 the moment it is seen, so the read is one pass over at most ten keys and the
  gateway prices and scopes exactly what the service reads. The forwarded `model` is always
  the priced route's upstream id; a model asked in any case is filed as its route id.
- **A handle call holds what its handle bills.** `handled` remembers, per org-scoped
  handle, the input tokens its observe and each later call billed, and a decision
  over it holds at least that — its own body is a few bytes.
- **A hold is at least a cent, spend is carried in nano.** The ledger holds cents and
  a decision costs a fraction of one: `decide` holds `max(1¢, estimate)` and settles
  with `settleNano`; `BalanceLedger` carries the sub-cent remainder, which counts
  against what is available as the cent it has begun. A cent covers a whole wire's
  worth of state at either price.
- **The reply never waits on the books.** The debit goes to 16 settlers through a
  1024-deep queue (full: filed inline, never dropped). A debit the ledger refuses or
  does not answer in `usageTimeout` (5 s) is tried 3 times with backoff; a retry after
  a lost ack is charged once: each usage record mints one `Ref` the first time it is
  filed (`object.UsageEvent.Ref`), and the host keys the ledger on it. Not the
  record's RequestID — a hedge's winner and its losers share that one.
  `controllers.Settled` waits `SettleBudget()`; cmd/aid stops taking requests first,
  and cloud's ai plugin runs it as its `Shutdown` hook after zip drains.
- **Held answers are per org.** `decisioncache.go` is an LRU of Kai answers for an
  identical body (whitespace-insensitive, order kept) from the same org within a
  minute, under a fresh `id`, billed like a miss. Each org holds at most an eighth of
  it and evicts its own. Never across orgs. Handle requests are never held.
- **`Restate` is the wording rule**: every refusal in the service's shape,
  `Retry-After` + `Retry-After-Ms` on 402/429/529, `X-Request-Id` on everything; the
  service's `X-Request-Id` passes through. The `Dialect` filter sits OUTSIDE
  `Recovered`, so a panic's 500 is worded too, and matches the path in any case,
  trailing slash or not. The ZAP cloud wire (MsgType 100) has no header slot:
  its replies carry status, body and error text only, never headers folded into a
  body. The usage row's `request_id` column is the id the caller saw
  (`ClientRequestID`); the row's own `id` stays minted here. A 502 names no address.
- **`created` on `/v1/models` is a release time where one is recorded**: a
  models.yaml `released:` (RFC 3339; kai and the Jev ids carry theirs) or an
  OpenRouter SKU's own `created`. A model nothing records keeps the listing's time;
  none is invented. The shape is unchanged (Codex decodes it).
- **`pricing` on `/v1/models` names its unit in every key**: `prompt`/`completion`
  are USD per token as decimal strings (OpenRouter's keys and unit);
  `input_per_million`/`output_per_million` are the same rates per 1M. A bare
  `input`/`output` is per token to OpenRouter- and Vercel-style readers, so no
  per-million figure goes there (hanzoai/pricing's own document keeps `input` per
  1M; it is not an OpenAI-compatible catalog).
- **A listed price is the billed price**: `build` prices every row with
  `getModelPriceForOrgOK(id, "")`, the lookup billing charges from, so a bare id an
  OpenRouter alias serves lists the SKU's retail, not its config figure. No price
  is listed where billing has none, nor for a variable SKU.
- **OpenRouter's routers are variable** (priced `-1`, e.g. `openrouter/auto`):
  premium, paid-floored, held at the catalog's dearest rate on each side, and billed
  at the answer's `usage.cost` × margin (the ceiling when no cost is stated). A
  catalog with nothing priced drops them.
- **`canonical_slug` identifies; `id` routes**: `owned_by/id` (`hanzo/kai`), the id
  when already qualified, absent for an unbranded passthrough and when another
  route owns that spelling. A published slug is refused (400), never taken by a
  family prefix (`zenlm/zen5` starts with `zen`) and billed at a default. Every
  entry path bills and gates on the raw id, so routing a slug needs
  canonicalizing at the entry first.
- **A UI reads a model's kind from `/v1/models`, never from its id**: every row
  carries `class` (`ClassOf`, the policy's own answer) and, for Hanzo's, `family`
  (`lineage`: enso/zen from `FamilyOf`, kai, jev, zoo). OpenRouter rows add `name`
  (vendor lead cut), `description`, `inputs`, `supports_tools`/`supports_reasoning`
  (from `supported_parameters`); family rows add `outputs` from their `mode` and
  `supports_vision`; our own routes state a `description` in models.yaml. Class and
  family resolve routes, so `build` stamps them after it stores the slugs —
  before, the first build waits on its own lock (`TestFirstBuildDoesNotWaitOnItself`).
- **Kai's versioned id** `kai-<first 12 lowercase hex of the weights' sha256>` is
  Kai with no route of its own: priced and filed as `kai`, sent to the
  service as asked (the service checks it names the weights it serves). Any other
  `kai-…` no route names is 400.
- **Nothing named Jev reaches Kai**: a route or a models.yaml `alias_of` that would
  send a Jev-named id to a Kai upstream is refused where it resolves (`Canonical`,
  `decisionModel`).
- **The spec is derived.** `routers/shape.go` reads `validate:"required,min=,max="`
  and `enum:"..."` tags, a type's `Schema()` (Content kinds) and `Variants()`
  (a question is one of three, by `type`); `controllers.Answer.Refusals` states
  each refusal and its headers.

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

**One address family** (`controllers/rag.go`): `POST /v1/ai/rag/ingest`,
`/v1/ai/rag/embed`, `/v1/ai/rag/query`, `/v1/ai/rag/query-multiple`,
`/v1/ai/rag/delete`, `GET /v1/ai/rag/context`. Ingest and embed are two ways in
to the one index; the rest read it back.

The one exception is `POST /v1/embed` (`controllers/rag_librechat.go`) — the
MULTIPART form hanzo.chat's `uploadVectors()` posts. Its request line is fixed
by that client, not by us, which is why it is not a spelling of
`/v1/ai/rag/embed`. Everything hanzo.chat READS it reads from the native family.

**Migration**: hanzo.chat's RAG client calls `${origin}/v1/ai/rag/*` directly.
Auth/billing reuse the doc-RAG path (`resolveSearchAuth`/`requireIndexAuth` +
`recordSearchUsage`); owner is the authenticated principal (tenant isolation),
never a client-supplied field.

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

- **Address lanes** (`address.Buckets`) — IPv4 whole; IPv6 its /64, then its /48 at
  16×, the wider charged only for what the narrower admitted. The router asks only
  the /64 before buying an IAM round trip for an unresolved key, and charges the /48
  only to a caller who stays anonymous, so a neighbour cannot close a paying key's
  /48. A resolved key stays named past its 5-minute TTL (up to 2×) while one
  background re-check replaces it. The free lane's DAY holds a /48 to `siteDay`
  (256) visitors, not 16: sixteen strangers a day is a quiet carrier /48.

- **Single-request reservation** (`controllers/billing_reserve.go`) — an uncapped
  `max_tokens` is clamped (`clampMaxTokens`) and the reservation covers the larger
  of the clamped ceiling and the QueryText pipeline's fixed completion cap
  (`reserveCompletionTokens` / `reserveCompletionFloor=4096`, mirrored in
  `model/openai_util.go`), so actual spend can never exceed the hold on any path.

- **Native `/v1/messages` is the caller's bytes** (`nativeRequest`, HTTP and ZAP,
  text-only included — QueryText never serves a native upstream) — only `model`
  and `max_tokens` (the reserved ceiling) are spliced; `anthropic-version`/`-beta`
  forwarded; only content-type and our `X-Request-Id` come back. `splice` refuses a
  top-level key repeated or case-folded (encoding/json folds case, keeps the last).
  The shared account serves an ALLOWLIST (`sharedFields`/`sharedTools`/
  `sharedBetas`, no held `file_id`); a BYO account is sent anything. The hold is
  body bytes/3.5 + ceiling; an answer without the vendor's final count bills no
  less (`lost`); upstreams get an idle deadline, not a total one; no SSE line cap.
  Billing: SKU price, every `usage.iterations` run, cache writes 1.25x (5m) / 2x
  (1h, folded into write units until usageRecord carries a 1h count). A stream
  takes its hold with `carry` (anthropic + family), or the handler's `settle(0)`
  wins.

- **A tier is read through `object.TierReader`, and the host owes a reader that
  answers** — there are two consumers and they must ask in the same order:
  `controllers.commerceFamilyTier` (the per-SKU gate) and
  `routers.TierCache.commerceTierLookup` (the rate limiter). Both read the
  installed reader FIRST; the HTTP route is what a STANDALONE ai has instead of
  one, not a second way to ask. `InitTierCache` builds the cache when EITHER
  exists, not when the endpoint alone is set.

  The reader is NOT necessarily in-process. Every cloud app is its own child
  PROCESS, so a Go func var cannot cross that boundary: what cloud installs is
  `metering.Client.Tier`, which is an HTTP call to
  `GET /v1/billing/tier` carrying `COMMERCE_SERVICE_TOKEN` and `X-Org-Id`. That
  call has no user session, so it only succeeds where the handler resolves its
  tenant with billing's `readerOrg` (session OR trusted service naming an org)
  rather than `principal.OrgFrom` alone. `balance` had that rule; the typed ops
  behind `tier` did not, and answered `401 sign in to view finance` to a token the
  same request proved good on `/v1/billing/balance`. Fixed in cloud
  `apps/billing/typed.go` (`payer` falls through to `readerOrg`), guarded by
  `apps/billing/tier_s2s_test.go`.

  **The failure is silent in both directions**, which is why it lasted: the rate
  limiter maps a failed read to `TierZenFree` (every paying org shaped as free)
  and the SKU gate maps one to unknown and ALLOWS (it stops gating). Neither logs
  an error anyone watches, so assert the tier a caller actually gets rather than
  that the lookup "worked".

- **A failed call bills only what a vendor actually ran** — `recordUsage` bills a
  failure because by then the vendor has run the request and invoiced us. That
  premise does not hold for a refusal it can decide from the request alone (400,
  401, 402, 403, 404, 413, 422, 429) or for an error carrying no status at all
  (nothing was dialled). `controllers.ran(err)` answers that, and it is NOT
  `faultOf`: faultOf asks where the request goes NEXT, and the two come apart both
  ways — a 401 fails over having run nothing, a partial write stops the cascade
  having run plenty. `spent()` consults `ran` only when nothing came back, so a
  meter the vendor reported and any text that reached the caller still settle it
  first. Without this the quantity billed is the prompt measured on the way out,
  so the charge grows with the request: 400k tokens refused for being too long
  billed a dollar.

- **Exemption is by ROUTE, never by identity.** `BALANCE_EXEMPT_USERS` is read
  by nothing; `TestEnforceBalanceGate_NoExemptBypass` and
  `TestCheckBalanceNoExemption` set it and require the formerly-listed subjects
  to fail closed. What is exempt is a set of paths — `isBalanceExempt` in
  `routers/filter_balance.go` — plus `balanceExemptNames`, three usage reads
  named as POLICY rather than URL so a $0 org can still see the panel telling it
  to add credit, and so moving the route cannot silently re-gate them. Exempt
  from balance is not exempt from auth.

- **SuperAdmin** — `util.IsSuperAdmin` = `owner == util.AdminOrg`, the constant
  `"admin"`. Membership in that one reserved IAM org is the whole definition:
  no list, no env override, and `isAdmin` is not part of it — an org admin
  (`hanzo/z`) administers `hanzo` and holds no platform power.

- **Authz gate** (`routers/authz_filter.go`) — platform-sensitive endpoints
  (`superAdminEndpoints`) are gated FIRST (before preview-mode + exempt reads).
  There is deliberately NO `adminDomain` Host bypass (a removed full-authz-bypass
  primitive). No principal → 401; wrong principal → 403.

- **User redaction** (`object/redact.go`) — `RedactUserSecrets` is an allowlist
  projection over string/[]string fields (`exposableUserFields`): a field not on
  the list is zeroed, so a NEW upstream secret field is fail-secure. Credential
  struct slices (MfaAccounts/ManagedAccounts/MfaItems/FaceIds) are nilled.

## Cost: known, zero, or not known — three answers, never two

An unregistered COGS used to read back as the CUSTOMER PRICE. `costInputPerMillion()`
returned the price when no cost rate was set, so `tokenProviderCostNano` computed cost
== billed and every such call recorded `margin_nano == 0`. For the Enso family that was
every call: 1,296 rows checked, all of them zero. It is not a rounding error — it is a
ledger reporting a business that earns nothing on everything it sells.

The shape of the fix is that **cost is optional and margin follows it**:

- `controllers/model_pricing.go` — `modelPrice.costed()` reports whether the model
  states what it costs us, and it requires BOTH legs. Half a COGS is not a COGS: an
  input rate with no output rate prices the completion — the expensive half — at zero.
  `costInputPerMillion()` / `costOutputPerMillion()` now return the configured rate and
  nothing else; there is no path that reads one without first asking `costed()`.
- `controllers/billing_nano.go` — `tokenProviderCostNano` and `providerCostNano` return
  `*int64`, nil where no cost is known. `costMargin.CostNano` is a pointer for the same
  reason `MarginNano` already was: the row has to be able to say it does not know, and
  an int64 can only say zero. `usageMargin` reports a margin only when both sides are
  real.
- `controllers/zap_native.go` + `object/cloud_usage.go` — the warehouse column is
  `Int64`, so an unknown cost writes 0 and sets `uncosted = 1` beside it. Query
  `uncosted = 0` for rows whose cost is a fact.
- `controllers/telemetry.go` — the span emits `provider_cost` only when there is one,
  the way it already treated margin. It no longer asserts a COGS it does not have.

**Zero and unknown are different answers.** Zero is a fact: BYO (the customer paid the
upstream with their own key), a free route, speech on hardware we already own. Nil is
the absence of one: image and video have a per-unit PRICE table and no vendor invoice
behind it, and a token model that states no COGS has told us nothing.

**Speech sells at the published rates, in nano.** `sttNanoPerSecond` (100,000 =
$0.006/min) and `ttsNanoPerChar` (15,000 = $15 per 1M characters) are hanzo.ai/pricing's
Speech-to-Text and Text-to-Speech rows, keyed by every id that reaches the speech
service. Nano because a dictated sentence is seconds long and a per-call cent would bill
it at twelve times the rate. The speech routes carry no token price, so the balance gate
asks for a funded caller.

**Three speech doors, one service.** `/v1/audio/transcriptions` and `/v1/audio/speech`
are controllers; `/v1/audio/transcript` (the growing transcript) is the ZAP gateway
handler in `zap_transcript.go`, bound to HTTP by `AudioTranscript` through the
in-process bridge; `/v1/voice` is hanzoai/voice behind `zip.AdaptNetHTTP`, which takes
the upgrade's connection (zip v1.37.26+). The socket's ticket is its credential, so
`voice` is anonymous at the bearer filter; its gate trusts `object.TrustedJWTIssuers`
(IAM_URL is where IAM is reached, not what it signs as) and admits the browser origins
in `VOICE_ORIGINS`. The speech provider row `speech` is `operated`: the paid lane being
off never refuses it.

**The visitor's mic is a public lane** (`controllers/public_scribe.go`,
`POST /v1/audio/transcriptions/public`): no credential, the model assigned
(`zen-scribe`), the audio held to 60 s by the speech service's `max_seconds` (it stops
decoding just past it, `stt.WithLongest`), and a day per visitor in its own `dayCount`
keyed by `Lanes` (the /64 and the /48 at `siteDay`). `PUBLIC_SCRIBE_DAILY` is the
switch and the ceiling; 0 closes it. It never reaches a vendor: a `zen-scribe` route to
a provider that is not `operated` closes the lane. Nothing is kept or logged.

### Where a cost is registered

`object.ModelRoute.CostInPerMillion` / `CostOutPerMillion` — the route row, which the
admin cockpit already reads and writes. It is the only place, and the configured PRICE
tables are deliberately not consulted as a substitute.

The family leg used to make that unreachable for zen/enso: `familyModelPrice` returned
first and short-circuited the resolver, and the family wire (`zenWireModel`) carries no
cost field, so an enso model could not be given a COGS at all without restating its
price. `getModelPriceForOrgOK` now completes an uncosted family price from
`registeredCost`. Price and cost resolve independently, because they are independent
facts: the family owns what we charge, the route row owns what it costs us.

### What still has no COGS

The mechanism no longer lies, but a rate nobody has entered is still a rate nobody has
entered. Enso's real figures are known and live in the Enso catalog: `1.392 / 2.784`
$/MTok on the `deepseek-v4-pro` anchor (enso, enso-ultra) and `0.112 / 0.224` on
`deepseek-4-flash`. Against billed 4/20 that is roughly 2.9x in / 7.2x out.

Registering them is an operator action on the route row — set both legs; one leg alone
is ignored by `costed()`. Until then the rows read `uncosted = 1`, which is the honest
answer and a visible one: `SELECT count() FROM hanzo.cloud_usage WHERE uncosted = 1`
is the backlog, and it should fall as rates are entered.

For enso-ultra the fan-out means COGS is the sum of the arms that ran, which only the
server can total. `controllers/trace_export.go` carries `BilledNano`/`CostNano` and
surfaces as `CostNanoExact`; a stated cost wins over the rate table in `usageMargin`.
Rate math cannot express a fan-out cost at all, so that is the right seam for it.

The admin cockpit's below-cost check compared price against a "cost" that WAS the price,
so its difference was always exactly zero and it could neither fire honestly nor stay
quiet honestly. It now has a cost to compare against, or a flag saying there is none.


## Who pays is the host's call — `object.Limits` (`routers/filter_balance.go`)

On every chat path and `/v1/decisions` the gate asks the host's usage policy
(`object.LimitFunc`, cloud `apps/ai/limits`) with the model's `Class`
(`controllers.ClassOf`: free when it costs nothing, ours for Enso/Zen/the decision
service/`owned_by: hanzo`, premium otherwise) and whether it is `Priced`. The
answer is a `LimitGrant` with `Pays`:

- `plan` / `free` — **covered**: `Cover` puts the grant on the request locals AND
  context; `reserveFor`, `enforceBalanceGate` and the decision hold skip the
  wallet; `usageRecord.bind` picks the grant up from the context, the record
  carries `Plan`, and `recordUsage` settles `planUse` (list price; COGS for a
  model sold at zero) against the grant.
- `prepaid` / `credits` — the wallet gates as before; `Cash` on the grant rides
  the record to `UsageEvent.Cash` so the host draws cash only.

A `LimitHit` refuses by `Code` with no figure: 429 `usage_cap_exceeded` (a plan
window, the free lane included — limited mode cannot be farmed), 429
`free_plan_cap`, 402 `plan_allowance_used`, 402 `paid_plan_required`, 402
`model_cap` (the model used its share of the plan; `Model` names it or its
pattern, `Fallback` the Hanzo model that answers instead). A conversation from a
signed-in app (token `aud`) or a client sending `X-Hanzo-Fallback: allow` is handed
on instead — `model_cap` to `Fallback`, the other two to `FreeModel` — marked
`X-Hanzo-Fallback` and `X-Hanzo-Usage-Reason`, and the model it lands on is asked of
the policy again (up to three asks). Refusal actions: `upgrade`, `switch` (to the
fallback), and `credits` ("Continue with credits", which turns on the org's
`PUT /v1/ai/limits` opt-in) when `Credits` says the payer holds what could pay,
else `topup`. Served calls carry `X-Hanzo-Usage`, `X-Hanzo-Usage-Class`,
`X-Hanzo-Paid-By` (plan|credits|free — prepaid reads `credits`). An unreadable
policy decides nothing (the wallet gates). ZAP twins have no gate verdict, so they
stay wallet-only.

`auto`, `zen-router` and a request naming no model are the router's to resolve
(`isAutoModel`), and `routable` is the one eligibility rule it folds into `Known`
and the last-resort `Allow` floor: with an org enabled-models allowlist, exactly
what it names; without one, only Hanzo classes (ours + free). A premium model
answers `auto` only when the org allowlisted it; otherwise a caller names it.

## The paid lane has ONE switch — the zen catalog's `paid` line

`controllers.FreeOnly` is the only switch, and ai does not own it: the host sets it
to the loaded zen catalog's (cloud: `aicontrollers.FreeOnly = z.Free`), which is free
unless the catalog, universe `charts/app/files/zen/catalog.yaml`, says `paid: true`.
Spending is an opt-in: unset by a host, the paid lane is off. GitOps is the source of
truth; there is no settings row, no `FREE_ONLY` env and no second line (the enso service
reads the same line through `ZEN_SWITCH`). While it is off, no chat request reaches a
priced route, and the routes that stand in for it answer, named as what they are; a
tool or media request for one, and every embeddings, rerank, image, audio and video call
to a provider that is not a family's own service (`paying`), is refused, and so is a
decision that names Jev. Only zen and enso, whose catalogs decide for themselves, serve.

## The free pool — every OpenRouter account, spent as one

The Free plan is limited usage from ONE pool every free user shares: the
OpenRouter accounts named by `object.OpenRouterKeys` (`OPENROUTER_API_KEY`,
`_2`, `_3`, resolved KMS-first), spent by `controllers/family_keys.go`.

- **A free route turns the ring; a priced route does not.** OpenRouter's free
  allowance is per ACCOUNT (20/min; 50/day, or 1000/day once $10 of credit was
  bought), so a free request starts one key further round each time. A priced
  request starts at the first key, the funded one. An admin row's single key is
  sent as before and never benched (one tenant's 402 must not bench it for all).
- **A 429 is read, not guessed.** `freeQuota` benches a key for free routes only
  when the message STARTS `Rate limit exceeded: free-models-per-min|day`, until
  `X-RateLimit-Reset` (header or `error.metadata.headers`, ms epoch), else the
  window's own length; a bench is never shortened. Any other 429 on a FREE route
  is the model limited upstream: answered after one account (asking all three is
  three calls for one refusal) and the pool moves it to the next route. On a
  priced route it still asks the next account.
- **`FreePool()` is the pool's standing**: state (`available` | `busy` |
  `exhausted`) and when it refills, per process. Cloud's `ai_pool` plane op
  publishes state + refill only (no account counts) and `GET /v1/allowance`
  carries it to the Free usage page.
- **A busy FREE route moves to the pool** (`fallback`): enso-auto on the enso
  service's one account answering 429 while three accounts sit idle made a shared
  pool a queue for one key. A busy PRICED route still stays itself.
- **The pool answers for itself only when an account is benched.** A free-lane
  request the pool could not serve gets `object.PoolRefused` (429 `pool_busy` /
  `pool_exhausted`, `upgrade_url` = `object.PayURL`, hanzo.ai/pay) only when
  `FreePool()` is not available; a walk that failed with every account ready
  (502s, nothing discovered) keeps its path to the route's alternates. Busy never
  carries a reset past a minute; exhausted carries the refill and
  `x-should-retry: false`. A caller on a paid plan is told to pick a paid model,
  not to upgrade. A person's own share spent is 402 `allowance_spent`
  (`object.AllowanceSpent`) naming the window; `object.SpentFunc` answers a
  `Standing`. All three codes are `billingNotice`: relayed, they are never moved
  to the pool.
- **Free is held for seconds, not minutes** (`familyFreeTTL`, `freeTierTTL`, 10s),
  and the rate limiter re-asks a free entry's tier: it is the one answer a
  payment changes, so a paying org is rated as what it bought by the time
  checkout brings it back.
