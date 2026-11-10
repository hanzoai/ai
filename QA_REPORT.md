# QA Automation Report: Model Routing & Failover (Issue #137)

**Target Repository:** `hanzoai/ai`  
**Bounty / Issue:** `#137 — [QA Bounty] [P0] Model Routing & Failover`  
**Engineer:** Senior Go Developer & QA Automation Engineer (Bounty Watcher Agent)  
**Date:** November 10, 2026  

---

## 1. Executive Summary
This QA report and test suite address **Issue #137** (`[QA Bounty] [P0] Model Routing & Failover`). The goal is to prove that the OpenAI-compatible gateway advertises a routable model catalogue, bounds a slow provider instead of hanging, rides out and surfaces rate limits, and — most importantly — fails over from an unavailable primary to a backup provider that serves the **same** request and is billed under the caller's SKU. Every behaviour is driven end to end through the real `ListModels` and `ChatCompletions` handlers over scripted upstreams; no test reaches a live vendor.

---

## 2. Test Coverage & Matrix

| Test Scenario | Endpoint / Surface | Covered Aspects | Status |
| :--- | :--- | :--- | :--- |
| **Models Catalogue Contract** | `GET /v1/models` | `200` + `application/json`; list envelope (`object:"list"`, empty compatibility `models[]`, `data` sorted by id); every advertised id resolves to a routable provider | **Passed** |
| **Provider Timeout → Failover** | `POST /v1/chat/completions` | Primary slower than the header bound → backup serves; the abandoned answer never reaches the caller; bounded time; SKU stamped; timed-out provider cooled | **Passed** |
| **Exhausted Timeouts → Bounded 503** | `POST /v1/chat/completions` | Every provider past the bound → `503` "unavailable", no partial answer, bounded time, zero charge | **Passed** |
| **429 Retry-Then-Failover + Metric** | `POST /v1/chat/completions` | `429` retried per policy (2 attempts) then failed over; `cloud_supply_refused{reason="refused"}` increments once; billed to the serving provider under the SKU | **Passed** |
| **All Providers 429 → Honest 503** | `POST /v1/chat/completions` | Both rate-limited → `503` naming the SKU; no vendor/`429` leak, no `choices`; both refusals counted; zero charge | **Passed** |
| **Primary 500 → Failover, Request Intact** | `POST /v1/chat/completions` | Primary `500` → backup receives every parameter (model remap, `temperature`, `top_p`, `max_tokens`, `stop`, `response_format`, system turn); SKU stamped; only the backup billed | **Passed** |
| **Connection Cut Before Any Answer** | `POST /v1/chat/completions` | Primary hijacks + closes (EOF, no status/byte) → treated as a provider fault → backup serves; provider cooled | **Passed** |
| **Stream Failover Before First Byte** | `POST /v1/chat/completions` (`stream:true`) | Primary `503` before the first byte → stream moves to the backup; one whole SSE answer (content frames, `finish_reason`, `[DONE]`); the refusal is never spliced in | **Passed** |

> **Status note.** All eight tests are validated passing. On **Windows** they require a serialized SQLite connection because of a pre-existing environment issue that equally affects the repository's own existing controller tests — see **§6 Known Environment Issues**. The committed suite is test-only; no production code is changed.

---

## 3. Implementation Details

We added a rigorous, deep integration test suite in `controllers/model_routing_failover_test.go` using the repository's native controller test harness (`visit`, `answering`, `sent`, `answered`) and the `newRelayWorld` end-to-end fixture (a real SQLite store, an IAM/JWKS double, a funded caller, and two scripted `httptest` upstreams — a primary `relay-a` and a fallback `relay-b` — wired through a DB `ModelRoute` SKU). Routing responses, cooldowns, operator counters and usage debits are asserted against the real code paths.

### Key Test File: `controllers/model_routing_failover_test.go`
- **`TestModelsCatalogIsTheAdvertisedRoutingTable`**: Dispatches `GET /v1/models` through the real `ListModels` handler, asserts `200` + `application/json`, unmarshals the `modelList` envelope (`object:"list"`, empty `models[]`, `data` sorted by id), and verifies each advertised id resolves through `GetModelConfig().ResolveRoute` to a routable provider.
- **`TestProviderTimeoutFailsOverToTheBackup`**: Bounds the relay's response-header wait (`headerTimeout`), makes the primary answer after the bound (`slowToAnswer`), and asserts the backup's answer reaches the caller, the abandoned "too late" answer does not, both providers were asked once, the request returned in bounded time, the envelope is stamped the SKU, and the timed-out provider is cooled.
- **`TestTimeoutsWithNobodyLeftAreAnErrorNotAHang`**: Makes both providers exceed the header bound and asserts a bounded `503` that says "unavailable", carries no `choices`, asked each provider once, and charged the caller nothing.
- **`TestRateLimitIsRiddenOutThenFailsOverAndRecorded`**: Sets a 2-attempt retry policy, makes the primary decline `429` and the backup complete, and asserts the primary was asked twice / backup once, the `cloud_supply_refused{provider="relay-a",reason="refused"}` counter moved by exactly one (retries happen inside the single attempt), the provider is cooled, and the debit names the serving `relay-b` under the SKU.
- **`TestEveryProviderRateLimitedIsAnHonest503`**: Makes both providers decline `429` and asserts a `503` that names the SKU and says "unavailable" without leaking vendor names or `429`, carries no `choices`, counted both refusals once each, and charged nothing.
- **`TestPrimary500FailsOverWithTheRequestIntact`**: Makes the primary answer `500`, sends a request rich with parameters (system turn, `temperature:0`, `top_p:0.5`, `max_tokens:256`, `stop:["END"]`, `response_format:json_schema`), and asserts the backup received every parameter under its own upstream model id, the system prompt survived, the envelope is stamped the SKU, the primary is cooled, and only the backup is billed.
- **`TestAConnectionCutBeforeAnyAnswerFailsOver`**: Makes the primary hijack and close the connection (EOF — no status, no byte), and asserts the request moves to the backup, both were asked once, and the vanished provider is cooled. EOF is used deliberately so the fault is recognized cross-platform.
- **`TestStreamFailsOverBeforeItsFirstByte`**: Makes the primary decline `503` before the first byte and the backup stream, and asserts the caller reads one whole SSE answer (content frames, `finish_reason:"stop"`, `[DONE]`) with the refusal never spliced in.

---

## 4. How to Run Tests

```bash
# Run the model routing & failover suite (all 8 tests)
go test -v ./controllers -run 'TestModelsCatalogIsTheAdvertisedRoutingTable|TestProviderTimeoutFailsOverToTheBackup|TestTimeoutsWithNobodyLeftAreAnErrorNotAHang|TestRateLimitIsRiddenOutThenFailsOverAndRecorded|TestEveryProviderRateLimitedIsAnHonest503|TestPrimary500FailsOverWithTheRequestIntact|TestAConnectionCutBeforeAnyAnswerFailsOver|TestStreamFailsOverBeforeItsFirstByte'

# Or the whole controllers package
go test ./controllers/...
```

On Windows, read **§6** first: the pure-Go SQLite driver needs a serialized connection or the store locks (`SQLITE_BUSY`) before route resolution.

---

## 5. Verification & Pull Request Submission
- **Branch Created:** `test/model-routing-failover` (from local `main`; no `git fetch` of the unmerged PR #156 branch).
- **Files Added:** `controllers/model_routing_failover_test.go`, `QA_REPORT.md`.
- **Production Code:** unchanged — `object/adapter.go` and all other non-test files are untouched. The PR is test-only.
- **PR Reference:** opened against `hanzoai/ai` referencing `Fixes #137`.

---

## 6. Known Environment Issues

### Windows + pure-Go SQLite: `SQLITE_BUSY` (database is locked)
**Symptom.** On a Windows machine without a C toolchain (`CGO_ENABLED=0`), the new tests — and the repository's **own existing** controller tests (e.g. `TestAVendorFaultFailsOverToTheFallback`) — fail with `400: model "relay-sku" is not available`. The underlying cause is `database is locked (5)` (`SQLITE_BUSY`) raised when the store is read during route resolution.

**Root cause.** `object/adapter.go`'s `open()` calls `dbx.MustOpen(driverName, rawPath)`, which reaches `sql.Open` with the **raw file path** and bypasses the SQLite driver's recommended DSN pragmas (`busy_timeout`, `journal_mode=WAL`, `synchronous=NORMAL`, `foreign_keys=ON`, `cache=shared`). With `CGO_ENABLED=0` the pure-Go `modernc` driver is selected; its multi-connection pool contends on the same file on Windows and returns `SQLITE_BUSY` instead of waiting. When the DB is locked at that moment, `ResolveModelRouteFromDB` errors, `resolveModelRoute` returns `nil`, and the handler answers `400 model not available`. Because the project's existing tests fail identically, this is an **environment/storage issue, not a defect in the new tests**.

**Validation workaround (NOT committed).** To prove the suite correct, the SQLite pool was temporarily serialized in `adapter.open()`:
```go
if a.driverName == "sqlite" {
    db.DB().SetMaxOpenConns(1)  // removes lock contention
    db.DB().SetMaxIdleConns(0)  // releases the file handle so Windows TempDir cleanup succeeds
}
```
With this, **all 8 new tests and the existing failover test pass**. The patch was **reverted before committing**, so the PR contains only the test file and this report.

**Recommended permanent fix (out of scope for this test-only bounty).** Have `adapter.open()` build the SQLite DSN through the driver's `DSN()` helper (which already emits `busy_timeout(10000)`, `journal_mode(WAL)`, `synchronous(NORMAL)`, `foreign_keys(ON)`, `cache=shared`) instead of passing the raw path. This is a production-code change left to the maintainers.

### Other reproduction notes
- **Toolchain:** Go 1.27 (`go.mod` requires `go 1.27.1`).
- **Module proxy:** use `GOPROXY=https://goproxy.cn,direct` (as `build.sh` does). `proxy.golang.org` returns `404` for `luxfi/crypto v1.20.2` and `zap-proto/mcp v1.0.5`, which breaks dependency resolution on the default proxy.
- **Linux / CI:** the project's Docker/CI build uses cgo (the `mattn/SQLCipher` driver), the canonical embedded store. The `SQLITE_BUSY` contention above is specific to the pure-Go `modernc` path taken when `CGO_ENABLED=0`; the suite was validated passing under the serialized-connection workaround described above.