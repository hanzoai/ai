# Cross-model API compatibility suite (#138)

One request shape, sent to every major model family over **both** wire formats the
gateway serves — OpenAI-compatible `POST /v1/chat/completions` and Anthropic-compatible
`POST /v1/messages` — with the response contract asserted on each answer:

| Scenario | What it asserts |
|---|---|
| `openai_sync` | `chat.completion` shape, `choices[0].message`, a mapped `finish_reason`, `usage` token counts |
| `openai_stream` | SSE layout: `data: {…}` chunks of `chat.completion.chunk`, `delta.content`, one terminal `finish_reason`, closing `data: [DONE]` |
| `openai_tools` | `tools` + `tool_choice`: `finish_reason: tool_calls`, `tool_calls[].{id,type,function.name}`, `function.arguments` parses as JSON |
| `openai_tools_parallel` | `parallel_tool_calls: true` yields two distinct calls in one turn |
| `openai_tool_followup` | multi-turn `role: "tool"` result round-trips and the answer comes back |
| `openai_stream_tools` | `delta.tool_calls` fragments carry an index and reassemble into complete, parseable calls |
| `openai_json_object` / `openai_json_schema` | `response_format` returns parseable JSON carrying the required keys |
| `anthropic_sync` / `anthropic_stream` | `type: message`, text content blocks, a mapped `stop_reason`, `usage.input_tokens/output_tokens`; the documented event sequence `message_start → content_block_start → content_block_delta(text_delta) → content_block_stop → message_delta → message_stop` |
| error envelopes | `{error:{message,type,param,code}}` on `/v1/chat/completions`, `{type:"error",error:{type,message}}` on `/v1/messages`, for 401 / 4xx-invalid-input / 404-unknown-model |

The matrix rows (`Matrix` in `matrix.go`) are the families named in #138. Ids are
pinned from the live catalog so `TestCatalog_Live` — which costs no credits, since
`GET /v1/models` is public — fails the moment a row drifts or a capability flag changes.

## Running it

```bash
go test ./qa/crossmodel -count=1                    # contract checks: no network, no key
HANZO_QA_LIVE=1 go test ./qa/crossmodel -run Live   # public live lane: catalog + error envelopes, no key
HANZO_API_KEY=hzo_xxx HANZO_QA_REPORT=/tmp/matrix.json \
  go test ./qa/crossmodel -run Live -timeout 30m -v # the full matrix; writes matrix.json + matrix.md
```

`HANZO_API_URL` retargets the gateway (default `https://api.hanzo.ai`, staging works too);
`HANZO_API_KEY` is the only credential needed. `hanzo/zen` is a `free` catalog model, so a
full keyed run can be kept off the bill.

A CI lane looks like this (the standing gate in `hanzo.yml` compiles and vets, which already
covers this package; the live lane is what needs a secret):

```yaml
  crossmodel:
    name: Cross-model API compatibility (#138)
    runs-on: [hanzo-build-linux-amd64]
    if: github.event_name == 'schedule' || github.event_name == 'workflow_dispatch'
    steps:
      - uses: actions/checkout@v4
        with: {fetch-depth: 1}
      - uses: actions/setup-go@v5
        with: {go-version-file: go.mod}
      - run: HANZO_QA_LIVE=1 go test ./qa/crossmodel -run Live -count=1   # key-free public checks
      - env:
          HANZO_API_KEY: ${{ secrets.HANZO_QA_KEY }}
          HANZO_QA_REPORT: matrix.json
        run: go test ./qa/crossmodel -run Live -timeout 40m -v
      - uses: actions/upload-artifact@v4
        with: {name: compatibility-matrix, path: matrix.*}
```

## Findings on `api.hanzo.ai` (2026-11-09, key-free run)

Reproduce with the `HANZO_QA_LIVE=1` line above; every item below is asserted by
`TestPublicErrorEnvelopes_Live` / `TestCatalog_Live`.

1. **401 on `/v1/chat/completions` is not an OpenAI error envelope.** Body is the console
   envelope `{"status":"error","msg":"authentication required: …","data":null,"data2":null}`.
   The official SDKs read `error.message`, so a first unauthenticated call dies on an
   undefined field instead of the actionable message. Status code (401) is correct.
2. **401 on `/v1/messages` is not an Anthropic error envelope either** — same console body,
   while Anthropic clients key off top-level `type:"error"` plus `error.type`.
3. **An unknown route 404s as `text/plain; charset=utf-8`, body `not found`** — no JSON, so
   no client can parse it at all.
4. **Catalog capability flags are omitted when false** (`supports_tools`, `supports_vision`,
   `supports_reasoning` appear only on the entries where they are true), while `free` and
   `premium` are always serialized. A matrix consumer cannot tell "false" from "unknown".
5. **Kai has no chat surface at all**: `hanzo/kai` is cataloged as `endpoint: /v1/decisions`,
   `outputs: ["decision"]`, so the identical chat payload #138 asks for cannot reach it. The
   matrix marks every Kai cell `skip` with that reason rather than pretending parity.
   Zen (`hanzo/zen`) and Enso (`enso-flash`) do answer `/v1/chat/completions` but are not
   flagged `supports_tools`, so their tool cells skip too — 511 models listed, 354 of them
   tool-capable.

Where the envelope fix belongs: once a request reaches a controller, both shapes are already
right (`controllers/anthropic_api.go` `anthropicErrorBody`, and `routers/filter_balance.go`
for `model_not_found`). The gap is the filter layer in front of it — `denyUnauthorized` in
`routers/base.go`, called from `routers/authz_filter.go`, answers every `/v1/*` credential
failure with the console envelope. One branch that picks the envelope by request path closes
findings 1 and 2; finding 3 is the router's no-match handler.
