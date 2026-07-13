# GenAI Serverless Inference: the model catalog does not match what the API serves

## Summary

We resell DigitalOcean GenAI through our own gateway, so we read `GET /v1/models`
to decide which model can serve a given request. That catalog currently disagrees
with the inference endpoint in four separate ways:

1. `context_length` is wrong — in **both** directions (one model serves 11x what it
   advertises; another serves 26% of what it advertises).
2. Roughly **half the catalog returns 403** "this model is not available for your
   account", including every OpenAI and every Anthropic model — while the web
   console lists them all as available *Serverless*.
3. Some models in the console (`ideogram-3.0-*`) are **not in `/v1/models` at all**,
   and some models in both (`fal-ai/*`) **404 on every endpoint**.
4. `qwen3-tts-voicedesign` fails inside your gateway on requests that pass your own
   schema validator.

Every result below was obtained by sending real requests to
`https://inference.do-ai.run/v1` on 2026-07-13 and recording the response.

---

## 1. `context_length` does not match the enforced limit

### 1a. `deepseek-v4-pro` serves ~11x its advertised window

Advertised: **87,040**.

| prompt tokens | result |
|---:|---|
| 300,005 | 200 OK |
| 500,005 | 200 OK (58s) |
| 800,005 | 200 OK (105s) |
| **1,000,005** | **200 OK (44s)** |
| 1,200,000 | 400 `invalid request body` — an HTTP payload limit, not a context error |

It never reports a context limit. A client trusting `context_length` would compact or
refuse prompts between 87K and 1M that the model handles without complaint.

### 1b. `nvidia-nemotron-3-super-120b` serves 26% of its advertised window

Advertised **1,000,000** (the console model page also says "Context length 1M").

| prompt tokens | result |
|---:|---|
| 250,017 | 200 OK |
| 260,017 | 200 OK |
| 500,000 | **400** |

The 400 states the real limit:

```
This model's maximum context length is 262144 tokens. However, you requested
8 output tokens and your prompt ...
```

### 1c. 262,144 appears to be a platform cap, not a model property

Five unrelated model families reject at exactly the same number:

| model | advertised | enforced |
|---|---:|---:|
| `nvidia-nemotron-3-super-120b` | 1,000,000 | **262,144** |
| `glm-5.2` | 262,144 | 262,144 |
| `qwen3-coder-flash` | 262,144 | 262,144 |
| `kimi-k2.5` | 256,000 | **262,144** |
| `mistral-3-14B` | 262,144 | 262,144 |
| `deepseek-v4-pro` | 87,040 | **not capped (1M+)** |

GLM, Qwen, Kimi, Mistral and Nemotron all landing on the identical ceiling is not a
property of those models. If 262,144 is an intentional serving cap, please document
it and report it in `context_length`; if it is not, Nemotron should serve the 1M it
advertises. Either way, `deepseek-v4-pro` shows the cap is not universal.

**Ask:** make `context_length` report the limit the endpoint actually enforces, per
model, per account.

---

## 2. Half the catalog is 403 for our account — but the console says otherwise

We probed all 68 models in `/v1/models`. **35 return 403**:

```
403 {"error":{"message":"this model is not available for your account",
              "type":"forbidden_error"}}
```

**Every Anthropic model (10):** `anthropic-claude-opus-4.5/4.6/4.7/4.8`,
`anthropic-claude-4.1-opus`, `anthropic-claude-4.5-sonnet`, `anthropic-claude-4.6-sonnet`,
`anthropic-claude-5-sonnet`, `anthropic-claude-fable-5`, `anthropic-claude-haiku-4.5`

**Every OpenAI model (21):** `openai-gpt-4o`, `openai-gpt-4o-mini`, `openai-gpt-4.1`,
`openai-gpt-5`, `openai-gpt-5-mini`, `openai-gpt-5-nano`, `openai-gpt-5.1-codex-max`,
`openai-gpt-5.2`, `openai-gpt-5.2-pro`, `openai-gpt-5.3-codex`, `openai-gpt-5.4`,
`openai-gpt-5.4-mini`, `openai-gpt-5.4-nano`, `openai-gpt-5.4-pro`, `openai-gpt-5.5`,
`openai-gpt-5.6-luna`, `openai-gpt-5.6-sol`, `openai-gpt-5.6-terra`, `openai-o1`,
`openai-o3`, `openai-o3-mini`, plus `openai-gpt-image-1`, `-1.5`, `-2`

**Also:** `arcee-trinity-large-thinking`

**What DOES work (33):** `deepseek-v4-pro`, `deepseek-3.2`, `deepseek-4-flash`,
`deepseek-r1-distill-llama-70b`, `glm-5`, `glm-5.1`, `glm-5.2`, `kimi-k2.5`, `kimi-k2.6`,
`llama-4-maverick`, `llama3.3-70b-instruct`, `mistral-3-14B`, `gemma-4-31B-it`,
`minimax-m2.5`, `mimo-v2.5`, `mimo-v2.5-pro`, `alibaba-qwen3-32b`, `qwen3-coder-flash`,
`qwen3.5-397b-a17b`, `nvidia-nemotron-3-super-120b`, `nemotron-3-ultra-550b`,
`nemotron-3-nano-omni`, `nemotron-nano-12b-v2-vl`, `openai-gpt-oss-120b`,
`openai-gpt-oss-20b`, `stable-diffusion-3.5-large`, `wan2-2-t2v-a14b`, and all
embedding/reranking models.

The pattern is exact: **the models that 403 are the paid third-party pass-through
models; the models you host yourself all work.**

Two problems with this:

- The **web console Model Catalog lists every one of them as available "Serverless"**,
  with per-token pricing. So does `/v1/models`. Neither surface indicates we cannot
  call them. We discover it only at request time, in production.
- **This may be the same root cause as our credits issue.** We have credits on the
  account, and they are not being applied to Serverless Inference. That the failures
  land precisely on the paid pass-through models suggests entitlement/billing, not
  policy. If credits do not authorize these models, that is worth saying explicitly —
  and if it is a bug, it is the one blocking us.

**Ask:** either (a) tell us what to do to enable these (agreement? separate billing?),
or (b) mark unentitled models in `/v1/models` (e.g. `entitled: false`) and in the
console, so we can route around them instead of 403ing at request time.

---

## 3. Models that exist in one surface but not the other

| model | console | `/v1/models` | callable |
|---|---|---|---|
| `ideogram-3.0-flash` / `-turbo` / `-default` / `-quality` | listed, priced | **absent** | 404 `this model is not a image generation model` |
| `fal-ai/flux/schnell` | listed, "Async" | present | **404** on `/v1/images/generations`, `/v1/images`, `/v1/videos` |
| `fal-ai/fast-sdxl` | listed, "Async" | present | **404** |
| `fal-ai/elevenlabs/tts/multilingual-v2` | listed, "Async" | present | **404** `this model is not a audio model` |
| `fal-ai/stable-audio-25/text-to-audio` | listed, "Async" | present | 404 |

The `fal-ai/*` models are marked **Async** in the console, which suggests a different
invocation path — but `POST /v1/images` returns a **DigitalOcean maintenance HTML page**
(not JSON), and no documented endpoint accepts them. If there is an async job API for
these, it is not discoverable from the API surface or the error messages.

**Ask:** document how `fal-ai/*` models are invoked, or remove them from `/v1/models`.
Add `ideogram-3.0-*` to `/v1/models` if they are meant to be callable.

---

## 4. `qwen3-tts-voicedesign` fails inside the gateway

Every well-formed request returns:

```
400 {"error":{"message":"the request could not be processed",
              "type":"invalid_request_error"}}
```

The request passes your own schema — the validator only complains when a field is
genuinely missing, and accepts `input` (1–512 chars) plus `voice` plus a legal
`response_format` from `[mp3, opus, aac, flac, wav, pcm]`. With all fields valid, it
fails anyway, with a generic message and no detail. We tried every documented voice id
and every output format.

Notably, your own schema for the `voice` field says:

> *"Voice id (OpenAI-compatible). For Qwen3 models the gateway overwrites this with
> `"default"` when calling the backend."*

So the value we send is discarded before it reaches the model, and the call then fails
regardless of what we send. This looks like a gateway-side defect, not a client error.

**Ask:** a working `curl` example for `qwen3-tts-voicedesign`, or a fix.

---

## Impact

We route across providers and select a model per request from its declared context
window and availability. With the catalog unreliable, we have had to replace it with a
measurement harness — sending real prompts at every model and recording where each one
breaks — which is slow, costs tokens, and must be re-run whenever you change a
deployment. Meanwhile our users could select any of 20 models that were advertised to
us and are guaranteed to 403.

A truthful `/v1/models` — enforced context window, and an entitlement flag — removes
all of that.

## Environment

- Endpoint: `https://inference.do-ai.run/v1`
- Date: 2026-07-13
- Account: hanzo (credits present; not being applied to Serverless Inference)
- Method: real requests per model; `chat/completions`, `embeddings`, `rerank`,
  `images/generations`, `videos`, `audio/speech` as appropriate to each model type.

---

# Reproduction

Everything below is copy-pasteable. Set your key once:

```bash
export DO_KEY="sk-do-..."
export DO="https://inference.do-ai.run/v1"
```

A helper that sends an N-token prompt to a model and prints what comes back. (The
prompt is the word `"word "` repeated N times, which tokenizes ~1:1 — the exact token
count is echoed back in `usage.prompt_tokens`, so no estimation is involved.)

```bash
probe() {  # probe <model> <n_tokens>
  python3 - "$1" "$2" <<'PY'
import json,os,sys,time,urllib.request
model, n = sys.argv[1], int(sys.argv[2])
body = json.dumps({"model": model,
                   "messages": [{"role":"user","content":"word "*n}],
                   "max_tokens": 8}).encode()
req = urllib.request.Request(os.environ["DO"] + "/chat/completions", data=body,
        headers={"Content-Type":"application/json",
                 "Authorization":"Bearer " + os.environ["DO_KEY"]})
t = time.time()
try:
    d = json.loads(urllib.request.urlopen(req, timeout=900).read())
    print(f"{model} @{n}: OK prompt_tokens={d['usage']['prompt_tokens']} ({time.time()-t:.0f}s)")
except urllib.error.HTTPError as e:
    print(f"{model} @{n}: HTTP {e.code}: {e.read()[:180].decode()}")
PY
}
```

Note: large prompts are frequently answered with `429 Platform overloaded` before they
are answered on their merits. That is throttling, not a limit — retry after ~15-30s and
the same request succeeds. Every result below is from a request that was not throttled.

## Repro 1a — `deepseek-v4-pro` accepts 1M tokens (advertised: 87,040)

```bash
probe deepseek-v4-pro 300000     # OK prompt_tokens=300005
probe deepseek-v4-pro 500000     # OK prompt_tokens=500005
probe deepseek-v4-pro 1000000    # OK prompt_tokens=1000005   <-- 11x the advertised window
probe deepseek-v4-pro 1200000    # HTTP 400: "invalid request body"  (payload limit, not context)
```

Compare against what the catalog claims:

```bash
curl -s -H "Authorization: Bearer $DO_KEY" "$DO/models" \
| python3 -c 'import sys,json; print([m for m in json.load(sys.stdin)["data"] if m["id"]=="deepseek-v4-pro"])'
# [{'context_length': 87040, 'created': 1777633630, 'id': 'deepseek-v4-pro',
#   'max_output_tokens': 17408, 'object': 'model', 'owned_by': 'digitalocean'}]
```

## Repro 1b — `nvidia-nemotron-3-super-120b` caps at 262,144 (advertised: 1,000,000)

```bash
probe nvidia-nemotron-3-super-120b 260000   # OK prompt_tokens=260017
probe nvidia-nemotron-3-super-120b 500000   # HTTP 400: "This model's maximum context
                                            #            length is 262144 tokens..."
```

## Repro 1c — the same 262,144 ceiling across unrelated families

```bash
for m in glm-5.2 qwen3-coder-flash kimi-k2.5 mistral-3-14B nvidia-nemotron-3-super-120b; do
  probe "$m" 300000     # each: HTTP 400 "...maximum context length is 262144 tokens"
done
probe deepseek-v4-pro 300000   # OK — the cap is not universal
```

## Repro 2 — half the catalog is 403

Probe every model in the catalog with a one-word prompt:

```bash
curl -s -H "Authorization: Bearer $DO_KEY" "$DO/models" \
| python3 -c 'import sys,json; [print(m["id"]) for m in json.load(sys.stdin)["data"]]' \
| grep -v '^router:' \
| while read m; do
    code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$DO/chat/completions" \
      -H "Authorization: Bearer $DO_KEY" -H 'Content-Type: application/json' \
      -d "{\"model\":\"$m\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}],\"max_tokens\":4}")
    echo "$code  $m"
  done | sort
```

Result: **35 x `403`**, and they are exactly the OpenAI + Anthropic + Arcee models.
Single case:

```bash
curl -s -X POST "$DO/chat/completions" \
  -H "Authorization: Bearer $DO_KEY" -H 'Content-Type: application/json' \
  -d '{"model":"anthropic-claude-opus-4.8","messages":[{"role":"user","content":"hi"}],"max_tokens":4}'
# {"error":{"message":"this model is not available for your account","type":"forbidden_error"},"status_code":403}
```

...while the console lists `anthropic-claude-opus-4.8` as **Serverless, $5.00/M input,
$25.00/M output**, and `/v1/models` returns it with no availability flag.

## Repro 3 — `fal-ai/*` and `ideogram-*`

```bash
# fal-ai IS in /v1/models, but 404s on every endpoint:
curl -s -X POST "$DO/images/generations" -H "Authorization: Bearer $DO_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"fal-ai/flux/schnell","prompt":"a red cube","n":1}'
# {"error":{"message":"this model is not a image generation model","type":"not_found_error"},"status_code":404}

curl -s -X POST "$DO/audio/speech" -H "Authorization: Bearer $DO_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"fal-ai/elevenlabs/tts/multilingual-v2","input":"hello","voice":"Rachel"}'
# {"error":{"message":"this model is not a audio model","type":"not_found_error"},"status_code":404}

# POST /v1/images returns an HTML maintenance page rather than JSON:
curl -s -X POST "$DO/images" -H "Authorization: Bearer $DO_KEY" \
  -H 'Content-Type: application/json' -d '{"model":"fal-ai/flux/schnell","prompt":"x"}' | head -3
# <!DOCTYPE html> <html> <head> <title>DigitalOcean - Maintenance</title>

# ideogram is in the console but not the API:
curl -s -H "Authorization: Bearer $DO_KEY" "$DO/models" | grep -c ideogram   # 0
```

For contrast, the image model that **does** work:

```bash
curl -s -X POST "$DO/images/generations" -H "Authorization: Bearer $DO_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"stable-diffusion-3.5-large","prompt":"a red cube","n":1}' \
| python3 -c 'import sys,json; d=json.load(sys.stdin); print("keys:",list(d)); print("image:",list(d["data"][0]))'
# keys: ['background','created','data','output_format','quality','size','usage']
# image: ['b64_json']
```

And text-to-video, which works but only via `POST /v1/videos` (not
`/v1/videos/generations`, which returns `405`):

```bash
curl -s -X POST "$DO/videos" -H "Authorization: Bearer $DO_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"wan2-2-t2v-a14b","prompt":"a red cube rotating"}'
# {"created_at":...,"id":"video_...","object":"video","status":...}   <- 202, async job
```

## Repro 4 — `qwen3-tts-voicedesign` fails on a schema-valid request

The validator confirms the request shape is correct — omit a field and it names it:

```bash
curl -s -X POST "$DO/audio/speech" -H "Authorization: Bearer $DO_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"qwen3-tts-voicedesign","input":"hello","voice":""}'
# ...Error at "/voice": minimum string length is 1
#    Schema: { "description": "Voice id (OpenAI-compatible). For Qwen3 models the
#               gateway overwrites this with `\"default\"` when calling the backend." }
```

Supply every field correctly and it still fails, with no detail:

```bash
for v in alloy nova echo shimmer Chelsie Ethan Cherry default; do
  curl -s -X POST "$DO/audio/speech" -H "Authorization: Bearer $DO_KEY" \
    -H 'Content-Type: application/json' \
    -d "{\"model\":\"qwen3-tts-voicedesign\",\"input\":\"hello world\",\"voice\":\"$v\",\"response_format\":\"mp3\"}"
  echo "  <- voice=$v"
done
# every one: {"error":{"message":"the request could not be processed",
#                      "type":"invalid_request_error"},"status_code":400}
```

Since the gateway discards `voice` anyway (per its own schema description), no client
input can affect the outcome.
