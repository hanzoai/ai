# QA #137 — Model routing & failover: the silent failures

This adds to the failover suites in #145 and #157 rather than repeating them. Those
cover vendors that **say** they failed (a 4xx/5xx status, an error frame, an error
object, a timeout). This covers vendors that fail **without saying so**: the caller
gets a success that isn't one. The issue's acceptance criteria ask for "zero
tolerance" on exactly this.

## Findings

### F1. A stream cut off mid-answer reaches the caller as a finished answer (P0)

`streamCaptureUsage` (`controllers/billing_reserve.go`) relays an upstream stream
until its body ends, then returns. If the vendor's connection drops after the
answer has begun, or the vendor closes its response before sending a
`finish_reason` or `[DONE]`, the relay's own response still closes cleanly. The
caller's SDK sees a normal end of stream and returns half an answer as if it were
the whole thing. Nothing tells the caller, and the request is billed for what was
relayed.

`TestAStreamNeverMovesAfterItsFirstByte` already pins down that a stream which
has begun must not be moved to another vendor, and that rule is right. What was
missing is telling the caller the stream broke.

**Fix:** track whether the answer said it was done (any `finish_reason`, or
`[DONE]`). If the upstream ends without either, and the caller is still connected,
end the stream with an OpenAI error frame,
`data: {"error":{"message":"…before the answer finished","type":"upstream_error","code":502}}`.
The OpenAI SDKs raise on this frame. Its shape is the one `openError` already
uses for streams that fail elsewhere (`awaitCommitted`). A warning is logged as
well. Billing is unchanged: the text that was relayed is still what bills. If
maintainers think a cut answer should be refunded instead, the change goes in
`settle`.

### F2. The scanner error in that loop is never checked (P0, same root cause)

The scanner's buffer is capped at 8 MB. A longer SSE line stops `scanner.Scan()`
with `bufio.ErrTooLong`. The comment above the function names this exact danger
("would be dropped without an error anyone sees"), but the code never checks
`scanner.Err()`, so the rest of the answer is silently lost. The F1 fix covers
this case too, and the log line includes the scanner error.

### F3. A buffered 200 with no answer in it is delivered and billed (P0)

In `pass.call` (`controllers/forward.go`), `refusedIn` moves a 200 only when its
body has an `error` object and no `choices`. A 200 with `"choices": []` and no
error, or with no JSON object at all, is relayed as a success:

- the caller is billed for the prompt (`settle` falls back to the measured prompt
  tokens);
- the caller gets an empty success;
- the fallback is never asked.

Examples of such bodies: an HTML page from a CDN or proxy, or `{}`.

**Fix:** `noChoices(body)` treats a body that isn't a JSON object, or whose
`choices` is missing or empty, as a 502 refusal, so the normal failover cascade
moves it. A choice whose content is `""` is still an answer and is still relayed
(see the control test). #145's `isJSONObject` covers the HTML case only;
`noChoices` includes it, so whichever merges second just drops the duplicate.

## Tests: `controllers/routing_silent_failure_qa_test.go`

All are hermetic. They use the package's own `newRelayWorld` / `script` doubles:
no network, no live vendor.

| Test | Proves | On `main` without the fixes |
|---|---|---|
| `TestAStreamCutMidAnswerTellsTheCallerItBroke` | connection dropped mid-answer → error frame, no `[DONE]`, hold released | fails (F1) |
| `TestAStreamThatStopsWithoutFinishingTellsTheCaller` | clean close with no `finish_reason` and no `[DONE]` → error frame | fails (F1) |
| `TestAnOversizedStreamLineIsReportedNotDropped` | a >8 MB line → error frame, earlier text still billed | fails (F2) |
| `TestABufferedAnswerWithNoChoicesFailsOver` | `choices: []` → fallback answers | fails (F3) |
| `TestNoChoicesFromEveryVendorIsNotABilledSuccess` | every vendor empty → not 200, 0 cents spent | fails (F3) |
| `TestAnHTMLPageBehindA200FailsOver` | HTML page served as 200 → fallback answers, page never reaches the caller | fails (F3) |
| `TestAStreamedFailoverCarriesTheWholeRequest` | streamed request moved to its fallback arrives byte-identical (system prompt, 7 turns, tools, `tool_choice`, `response_format` json_schema, `parallel_tool_calls`) under the fallback's own upstream id; caller only ever sees the SKU | expected to pass |
| `TestAFinishedStreamGetsNoErrorFrame` | control: a normal stream is unchanged | expected to pass |
| `TestAStreamThatFinishesWithoutDoneIsNotFlagged` | control: vendors that send `finish_reason` but no `[DONE]` aren't flagged | expected to pass |
| `TestAnEmptyStringAnswerIsStillAnAnswer` | control: `content: ""` with a choice is relayed, not moved | expected to pass |

```
go test ./controllers/ -run 'StreamCut|StopsWithout|Oversized|NoChoices|HTMLPage|StreamedFailover|FinishedStream|FinishesWithoutDone|EmptyStringAnswer' -count=1 -v
go test ./controllers/ -count=1
```

### Verified results (Windows 11, Go 1.27.3, 2026-11-10)

**Without the fixes** (main at `18eaf45`), the six bug tests fail on their own
assertions:

```
routing_silent_failure_qa_test.go:70:  a stream cut mid-answer ended as if it were whole — no error frame
routing_silent_failure_qa_test.go:96:  a stream with no finish_reason and no [DONE] ended silently
routing_silent_failure_qa_test.go:145: a line the scanner could not hold ended the stream silently
routing_silent_failure_qa_test.go:164: status 200, want the fallback's answer: {"choices":[], ... "model":"relay-sku" ...}
routing_silent_failure_qa_test.go:177: no vendor answered and the caller was told 200: {"choices":[], ...}
routing_silent_failure_qa_test.go:180: an answer nobody gave cost the caller 1 cents
routing_silent_failure_qa_test.go:194: status 200, want the fallback's answer: <html><body><h1>502 Bad Gateway</h1></body></html>
```

`:180` shows money taken for nothing. `:194` shows a proxy error page delivered
to the caller as a successful completion.

**With the fixes**, none of the 10 fails an assertion.

**Regression check** (the full `./controllers/` package, 2,040 tests). The package
was run once on `main` and twice with the fixes. Apart from the Windows cleanup
line described below, the only assertion failures came from the existing
Windows/SQLite flakiness. They differed from run to run: 5 tests in run 1,
another 2 in run 2, none in common, all
`model "relay-sku" is not available`, `failed to get provider … database …`,
or a 401 `crypto/rsa: verification error` from the IAM double. Each of those
tests passes when run on its own with the fixes. No existing test fails
consistently because of this change.

**Two things needed to reproduce this outside Hanzo:**

1. `go.mod` pins `github.com/luxfi/crypto v1.20.2` and
   `github.com/zap-proto/mcp v1.0.5`. Both repos are now private (GitHub API
   returns 404), and proxy.golang.org never cached those two versions, while
   v1.20.1/v1.20.3 and v1.0.4/v1.0.6 are cached. **So `main` cannot currently be
   built by any outside contributor.** To run the tests, a local, uncommitted
   `go.work` was used:
   ```
   go work init .
   go work edit -replace=github.com/luxfi/crypto@v1.20.2=github.com/luxfi/crypto@v1.20.3 \
                -replace=github.com/zap-proto/mcp@v1.0.5=github.com/zap-proto/mcp@v1.0.6
   ```
   Recommendation: publish those versions, or bump `go.mod` to versions that are
   public.
2. On Windows, every test that uses `withStore` is reported FAIL by
   `TempDir RemoveAll cleanup: … store.db: The process cannot access the file
   because it is being used by another process`, after its assertions have
   passed. Existing tests such as `TestAVendorFaultFailsOverToTheFallback` fail
   the same way. The SQLite handle isn't closed before `t.TempDir` cleanup.
   The results above ignore that line; on Linux it doesn't happen.

A test-isolation note: on unfixed `main`, a buffered empty answer that gets
**served** leaves state behind. The next relay test in the same process then
gets `400 model "relay-sku" is not available`. Each test passes or fails as
described above when run alone. With the fixes, the empty answer is never
served, and the whole file passes in sequence.

## Live probe: `docs/qa/137/live_probe.py`

This is a black-box check of a running gateway, which the hermetic tests can't
give: the real catalog, the real routes, the real vendors. It uses only the
Python standard library and reads the key from `HANZO_API_KEY`.

| Check | What it tests |
|---|---|
| catalog | `/v1/models` is JSON; every entry has an id; no id repeats case-insensitively |
| unknown | a model id nobody advertises is refused (4xx), never answered by another model |
| buffered | 200, JSON content type, non-empty `choices`, a `finish_reason`, content or a tool call, `model` field matches the id asked for |
| stream | event-stream content type; every frame is JSON or `[DONE]`; text arrives; a `finish_reason` arrives; ends with `[DONE]`; no error frame; one id; every chunk names the id asked for |
| `--features` | a forced tool call comes back as `tool_calls`; `response_format: json_object` comes back as parseable JSON |

Every request and response is written to a JSONL trace with `Authorization`
redacted, so any finding can be reproduced from the file alone.

```
python docs/qa/137/live_probe.py --catalog-only     # no key needed
python docs/qa/137/live_probe.py --limit 5          # billed: 2 short calls per model
python docs/qa/137/live_probe.py --all --features --out traces.jsonl
python docs/qa/137/test_live_probe.py               # self-test against a local fake gateway
```

`test_live_probe.py` runs the probe against a local fake gateway with one model
per silent failure: empty choices, HTML 200, substituted model on a buffered
reply and on a stream, cut stream, no `[DONE]`, error frame, and an unknown id
answered by another model. It asserts each is caught, the good model passes, and
the key never reaches the trace file. **It passes.**

`--catalog-only` against `https://api.hanzo.ai/v1` on 2026-11-10: **542 models,
every entry has an id, no duplicates.**
