# Security Policy

## Reporting a vulnerability

Email security@hanzo.ai with details. Encrypt with our PGP key (fingerprint TBD).

We respond within 48 hours. Critical issues receive same-day acknowledgment.

## Scope

This policy covers code in this repository. For the broader Hanzo platform threat model, see [hanzoai/HIPs](https://github.com/hanzoai/HIPs).

## Sandbox boundary

`ai` validates every request against Hanzo IAM (API key `hk-*` or JWT) and scopes routing, usage, and billing per the JWT-validated `X-Org-Id`. Provider API keys (OpenAI, Anthropic, Fireworks, DO-AI, ...) are resolved from `kms` per-org and never leave the binary; nothing about a tenant's prompt, response, or provider mapping is exposed across org boundaries.

For runtime sandbox guarantees, see HIP-0105 (in-process extension runtimes).
