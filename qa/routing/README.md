# Model Routing & Failover QA Suite (`#137`)

Automated P0 routing, silent-failure detection, and multi-hop failover test suite for **hanzoai/ai#137 (`[QA Bounty] [P0] Model Routing & Failover`)**.

## Coverage Matrix

| Requirement (`#137`) | Go Controller Suite (`controllers/model_routing_failover_qa_test.go`) | Standalone Harness (`qa/routing/model_routing_failover_qa.test.mjs`) | Status |
| :--- | :--- | :--- | :--- |
| `/v1/models` catalog fidelity & zero silent model substitution | `TestModelRoutingFailover_CatalogFidelityAndNoSilentSubstitution` | Verified primary route mapping | PASS |
| Silent 200 OK failure detection (empty content, HTML error pages, missing `finish_reason`) | Verified in stream/response guards | `1. Zero tolerance for silent 200 OK failures` | PASS |
| Multi-hop automatic failover on `429`, `503`/`504`, and silent truncation | `TestModelRoutingFailover_MultiHopFailoverTrailAndMetadata` | `2. Automatic multi-hop failover across 429, 504, and silent 200 truncation` | PASS |
| Ledger failover & fallback accounting (`books()`) | `TestModelRoutingFailover_LedgerFailoverAndFallbackAccounting` | Verified failover trail metadata | PASS |

## How to Run

```bash
# 1. Run hermetic Node.js QA suite
node --test qa/routing/model_routing_failover_qa.test.mjs

# 2. Run Go controller QA test suite
go test -v ./controllers -run "TestModelRoutingFailover_"
```
