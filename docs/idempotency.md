# Durable Idempotency & Billing Execution Ledger

Mutating HTTP requests (`POST`, `PUT`, `PATCH`, `DELETE`) carrying an `Idempotency-Key`
header are deduplicated against an authoritative SQL execution ledger (`idempotency_record`,
backed by SQLite/PostgreSQL via `dbx`) with optional Valkey/Redis KV acceleration (`KV_URL`).

## Canonical Scope & Request Fingerprinting

Every record is uniquely keyed by `(scope_hash, idempotency_key)` where `scope_hash` is a
length-prefixed SHA-256 over the **IAM-verified** caller and canonical operation:

```
(org, principal, project, auth_scope, operation)
```

`org` and `principal` are derived exclusively from verified IAM credentials (signed JWT,
IAM-verified `sk-*`/`pk-*` key, or in-process `object.Caller`), never from unverified client
headers. Unauthenticated mutations carrying `Idempotency-Key` are rejected with `401`.

The request fingerprint hashes `(method, normalized_route, canonical_query, content_type,
canonical_body)` with explicit 8-byte big-endian length prefixes to prevent delimiter
collisions. Reusing an `Idempotency-Key` with a different fingerprint returns `409 Conflict`
(`idempotency_key_conflict`).

## State Machine, Leases & Fencing

```
CLAIMED ──► RUNNING ──► COMPLETED
   │           │
   │           ├──► FAILED
   │           │
   │           └──► UNKNOWN ──► (reconcile) ──► COMPLETED | FAILED
   │
   └── (expired lease before side effects) ──► CLAIMED (fence_token++, same execution_id)
```

- Every claim mints one immutable `execution_id` (UUID) and `billing_ref` (`exec:<execution_id>`).
- Every state transition is fenced by `WHERE fence_token = {:token}`. A stale worker whose
  lease expired is rejected (`ErrIdempotencyFenced`) and cannot overwrite a newer worker's state.
- If a worker crashes **before** `RUNNING` (`SideEffectsStarted == false`), lease expiry allows
  safe takeover with `fence_token++` while preserving the exact same `execution_id` and `billing_ref`.
- If a worker crashes **after** `RUNNING` (`SideEffectsStarted == true`), lease expiry transitions
  the row to `UNKNOWN` (`fence_token++`) and returns `409 Conflict` (`idempotency_execution_unknown`)
  rather than silently re-executing a potentially billed operation.

## Billing Integrity

- Chat/completions, embeddings, decisions, audio, images, video, and fine-tune endpoints read
  `object.IdempotencyExecutionFromContext(ctx)` and pass the immutable `billing_ref` as `ref`
  and HTTP `Idempotency-Key` to Commerce.
- Fine-tune jobs persist an immutable `FinetuneJob.ExecutionId` UUID and meter with
  `finetune:exec:<execution_id>` (never mutable `owner/name` or `finetune:<subject>`).

## Replay, Streaming, Bounds & Fail-Closed Behavior

- Concurrent duplicates while a lease is active wait via synchronized channel/KV pub-sub
  notifications (no polling sleep) and either replay the finished response or receive
  `409 Conflict` (`idempotency_in_progress`, `Retry-After: 1`).
- Completed responses exceeding `MaxResponseBytes` (512 KiB) or streaming responses (including
  client mid-stream disconnects) persist `ResponseRetained = false` while keeping the durable
  `execution_id` and `billing_ref`. Retries return `409 Conflict`
  (`idempotency_response_not_retained`) and **never** re-execute.
- Failed responses after committed side effects record `SideEffectsCommitted = true` and replay
  the failed outcome without re-execution.
- Strict bounds: key length ≤ 256 B (`400`), request fingerprint body ≤ 4 MiB (`413`),
  retained response body ≤ 512 KiB, allowlisted headers ≤ 16 KiB, per-tenant active keys ≤ 10,000
  (`429`), L1 cache ≤ 64 MiB.
- **Fail closed**: if the authoritative SQL store is unavailable, mutating requests carrying
  `Idempotency-Key` fail closed with `503 Service Unavailable` (`idempotency_store_unavailable`).
- Optional KV (`KV_URL`) accelerates hot replays and cross-replica notifications; KV eviction
  or restart transparently falls back to SQL with zero duplicate execution.
