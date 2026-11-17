# Idempotency middleware

Mutating requests (POST/PUT/PATCH/DELETE) carrying an Idempotency-Key header are
deduplicated per org using an in-process LRU (idemStore) with a 10-minute TTL.

## Scope

Idempotency is per pod and is not shared across replicas: a retry that lands on
a different pod re-executes the request. Treat the LRU as a v1 single-pod
optimization; a shared store is the follow-up for cross-replica deduplication.
