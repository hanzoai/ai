#!/usr/bin/env python3
"""Live black-box probe for hanzoai/ai#137 — model routing & silent failures.

Talks to a running gateway (default https://api.hanzo.ai/v1) the way a customer's
SDK does, and checks what the hermetic Go tests cannot: that the real catalog,
the real routes and the real vendors behind them behave.

Checks
  catalog   GET /v1/models is JSON, every entry has an id, no id repeats
            (case-insensitively).
  unknown   A model id nobody advertises is refused (4xx), never answered by
            something else.
  buffered  Each model: HTTP 200, JSON content type, non-empty choices, a
            finish_reason, some content or a tool call, and a `model` field that
            names the id asked for (anything else is a substitution).
  stream    Each model: text/event-stream, every data frame JSON or [DONE], text
            arrives, a finish_reason arrives, the stream ends with [DONE], no
            error frame, one id and the asked-for model on every chunk.
  tools     (--features) a forced tool call comes back as tool_calls naming it.
  json      (--features) response_format json_object comes back as parseable JSON.

Every request and response is written to a JSONL trace (Authorization
redacted), so a finding is reproducible from the file alone.

Standard library only. The key is read from HANZO_API_KEY and never printed or
written. Each chat check is a 1-turn prompt capped at --max-tokens (default 16)
— but it is still billed. Start with --limit and widen once you know the cost.

  python live_probe.py --catalog-only
  python live_probe.py --limit 5
  python live_probe.py --models gpt-4o-mini,zen4 --features
  python live_probe.py --all --out traces.jsonl
"""
from __future__ import annotations

import argparse
import http.client
import json
import os
import random
import sys
import time
import urllib.parse
from dataclasses import dataclass, field

TRACE_BODY_LIMIT = 4096
PROMPT = "Reply with the single word: pong"
UNKNOWN_MODEL = "qa-137-no-such-model-%06d" % random.randint(0, 999999)


@dataclass
class Result:
    model: str
    check: str
    ok: bool
    detail: str = ""
    ms: int = 0


@dataclass
class Probe:
    base: str
    key: str | None
    timeout: float
    trace: object
    results: list[Result] = field(default_factory=list)

    # ── transport ──────────────────────────────────────────────────────────

    def _conn(self) -> tuple[http.client.HTTPConnection, str]:
        u = urllib.parse.urlsplit(self.base)
        cls = http.client.HTTPSConnection if u.scheme == "https" else http.client.HTTPConnection
        return cls(u.netloc, timeout=self.timeout), u.path.rstrip("/")

    def _headers(self, stream: bool = False) -> dict[str, str]:
        h = {"Content-Type": "application/json", "User-Agent": "hanzo-qa-137-probe/1.0",
             "Accept": "text/event-stream" if stream else "application/json"}
        if self.key:
            h["Authorization"] = "Bearer " + self.key
        return h

    def _record(self, method, path, body, status, headers, raw, ms, note=""):
        if not self.trace:
            return
        text = raw.decode("utf-8", "replace") if isinstance(raw, bytes) else raw
        if self.key:
            text = text.replace(self.key, "[REDACTED]")
        self.trace.write(json.dumps({
            "at": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
            "request": {"method": method, "url": self.base + path, "headers": {"Authorization": "[REDACTED]"} if self.key else {},
                        "body": body},
            "response": {"status": status, "headers": headers,
                         "body": text[:TRACE_BODY_LIMIT], "truncated": len(text) > TRACE_BODY_LIMIT},
            "ms": ms, "note": note,
        }) + "\n")
        self.trace.flush()

    def request(self, method: str, path: str, body: dict | None = None):
        conn, prefix = self._conn()
        payload = json.dumps(body).encode() if body is not None else None
        t0 = time.monotonic()
        try:
            conn.request(method, prefix + path, body=payload, headers=self._headers())
            resp = conn.getresponse()
            raw = resp.read()
            ms = int((time.monotonic() - t0) * 1000)
            headers = {k.lower(): v for k, v in resp.getheaders()}
            self._record(method, path, body, resp.status, headers, raw, ms)
            return resp.status, headers, raw, ms
        finally:
            conn.close()

    def stream(self, path: str, body: dict):
        """Returns status, headers, the list of data payloads, whether the socket
        ended cleanly, and ms."""
        conn, prefix = self._conn()
        t0 = time.monotonic()
        frames: list[str] = []
        raw_lines: list[str] = []
        clean = True
        status, headers = 0, {}
        try:
            conn.request("POST", prefix + path, body=json.dumps(body).encode(), headers=self._headers(stream=True))
            resp = conn.getresponse()
            status, headers = resp.status, {k.lower(): v for k, v in resp.getheaders()}
            if status != 200:
                raw = resp.read().decode("utf-8", "replace")
                raw_lines.append(raw)
            else:
                while True:
                    try:
                        line = resp.readline()
                    except (http.client.IncompleteRead, ConnectionError, TimeoutError) as e:
                        clean = False
                        raw_lines.append(f"<<transport error: {type(e).__name__}: {e}>>")
                        break
                    if not line:
                        break
                    s = line.decode("utf-8", "replace").rstrip("\r\n")
                    raw_lines.append(s)
                    if s.startswith("data:"):
                        frames.append(s[5:].strip())
        finally:
            conn.close()
        ms = int((time.monotonic() - t0) * 1000)
        self._record("POST", path, body, status, headers, "\n".join(raw_lines), ms, note="stream")
        return status, headers, frames, clean, raw_lines, ms

    def add(self, model, check, ok, detail="", ms=0):
        self.results.append(Result(model, check, ok, detail, ms))
        mark = "PASS" if ok else "FAIL"
        print(f"  [{mark}] {check:<9} {model:<40} {ms:>6}ms  {detail}")

    # ── checks ─────────────────────────────────────────────────────────────

    def catalog(self) -> list[str]:
        status, headers, raw, ms = self.request("GET", "/models")
        if status != 200:
            self.add("-", "catalog", False, f"GET /models -> {status}", ms)
            return []
        ct = headers.get("content-type", "")
        try:
            doc = json.loads(raw)
        except ValueError:
            self.add("-", "catalog", False, f"not JSON (content-type {ct!r}): {raw[:120]!r}", ms)
            return []
        data = doc.get("data") if isinstance(doc, dict) else None
        if not isinstance(data, list):
            self.add("-", "catalog", False, "no data[] list", ms)
            return []
        ids, missing = [], 0
        for m in data:
            mid = m.get("id") if isinstance(m, dict) else None
            if isinstance(mid, str) and mid.strip():
                ids.append(mid)
            else:
                missing += 1
        seen: dict[str, str] = {}
        dups = []
        for mid in ids:
            k = mid.strip().lower()
            if k in seen:
                dups.append(f"{seen[k]} / {mid}")
            seen.setdefault(k, mid)
        ok = missing == 0 and not dups and bool(ids)
        detail = f"{len(ids)} models"
        if missing:
            detail += f", {missing} without an id"
        if dups:
            detail += f", duplicates: {dups[:5]}"
        self.add("-", "catalog", ok, detail, ms)
        return ids

    def unknown(self):
        body = {"model": UNKNOWN_MODEL, "max_tokens": 4, "messages": [{"role": "user", "content": PROMPT}]}
        status, _, raw, ms = self.request("POST", "/chat/completions", body)
        if status == 200:
            served = ""
            try:
                served = json.loads(raw).get("model", "")
            except ValueError:
                pass
            self.add(UNKNOWN_MODEL, "unknown", False, f"answered 200 by {served!r} - silent substitution", ms)
        else:
            self.add(UNKNOWN_MODEL, "unknown", 400 <= status < 500, f"refused {status}", ms)

    def buffered(self, model: str, max_tokens: int):
        body = {"model": model, "max_tokens": max_tokens, "messages": [{"role": "user", "content": PROMPT}]}
        status, headers, raw, ms = self.request("POST", "/chat/completions", body)
        problems = []
        if status != 200:
            self.add(model, "buffered", False, f"status {status}: {raw[:160]!r}", ms)
            return
        ct = headers.get("content-type", "")
        if "json" not in ct:
            problems.append(f"content-type {ct!r}")
        try:
            doc = json.loads(raw)
        except ValueError:
            self.add(model, "buffered", False, f"200 but not JSON: {raw[:120]!r}", ms)
            return
        choices = doc.get("choices") or []
        if not choices:
            problems.append("200 with no choices")
        else:
            ch = choices[0]
            msg = ch.get("message") or {}
            if not ch.get("finish_reason"):
                problems.append("no finish_reason")
            if not (msg.get("content") or msg.get("tool_calls") or msg.get("reasoning_content")):
                problems.append(f"empty answer (finish_reason={ch.get('finish_reason')!r})")
        served = doc.get("model")
        if served != model:
            problems.append(f"model field {served!r} != asked {model!r}")
        if "error" in doc:
            problems.append("error object inside a 200")
        self.add(model, "buffered", not problems, "; ".join(problems) or _route_headers(headers), ms)

    def streamed(self, model: str, max_tokens: int):
        body = {"model": model, "max_tokens": max_tokens, "stream": True,
                "messages": [{"role": "user", "content": PROMPT}]}
        status, headers, frames, clean, raw, ms = self.stream("/chat/completions", body)
        if status != 200:
            self.add(model, "stream", False, f"status {status}: {' '.join(raw)[:160]!r}", ms)
            return
        problems = []
        if "text/event-stream" not in headers.get("content-type", ""):
            problems.append(f"content-type {headers.get('content-type')!r}")
        if not clean:
            problems.append("transport cut")
        text, finish, done, ids, models = "", None, False, set(), set()
        for f in frames:
            if f == "[DONE]":
                done = True
                continue
            try:
                ev = json.loads(f)
            except ValueError:
                problems.append(f"non-JSON frame {f[:60]!r}")
                continue
            if ev.get("error"):
                problems.append(f"error frame: {json.dumps(ev['error'])[:120]}")
            if ev.get("id"):
                ids.add(ev["id"])
            if ev.get("model"):
                models.add(ev["model"])
            for ch in ev.get("choices") or []:
                d = ch.get("delta") or {}
                text += (d.get("content") or "") + (d.get("reasoning_content") or "")
                if d.get("tool_calls"):
                    text += "<tool_call>"
                if ch.get("finish_reason"):
                    finish = ch["finish_reason"]
        if not text:
            problems.append("no text streamed")
        if not finish:
            problems.append("no finish_reason (truncated?)")
        if not done:
            problems.append("no [DONE]")
        if len(ids) > 1:
            problems.append(f"{len(ids)} different ids in one stream")
        if models - {model}:
            problems.append(f"chunks name {sorted(models)} not {model!r}")
        self.add(model, "stream", not problems, "; ".join(problems) or f"finish={finish}", ms)

    def tools(self, model: str):
        body = {"model": model, "max_tokens": 64,
                "messages": [{"role": "system", "content": "You call tools when asked."},
                             {"role": "user", "content": "Look up the weather in London."}],
                "tools": [{"type": "function", "function": {"name": "get_weather", "description": "weather for a city",
                           "parameters": {"type": "object", "properties": {"city": {"type": "string"}},
                                          "required": ["city"]}}}],
                "tool_choice": {"type": "function", "function": {"name": "get_weather"}}}
        status, _, raw, ms = self.request("POST", "/chat/completions", body)
        if status != 200:
            # A model that does not do tools should say so with a 4xx, not a 200.
            self.add(model, "tools", 400 <= status < 500, f"refused {status}", ms)
            return
        try:
            msg = (json.loads(raw).get("choices") or [{}])[0].get("message") or {}
        except ValueError:
            self.add(model, "tools", False, "200 but not JSON", ms)
            return
        calls = msg.get("tool_calls") or []
        names = [c.get("function", {}).get("name") for c in calls]
        ok = "get_weather" in names
        self.add(model, "tools", ok, f"tool_calls={names}" if calls else "forced tool call came back as plain text", ms)

    def json_mode(self, model: str):
        body = {"model": model, "max_tokens": 64, "response_format": {"type": "json_object"},
                "messages": [{"role": "system", "content": "Answer in JSON."},
                             {"role": "user", "content": 'Return {"ok": true} and nothing else.'}]}
        status, _, raw, ms = self.request("POST", "/chat/completions", body)
        if status != 200:
            self.add(model, "json", 400 <= status < 500, f"refused {status}", ms)
            return
        try:
            content = (json.loads(raw).get("choices") or [{}])[0].get("message", {}).get("content") or ""
            json.loads(content)
            self.add(model, "json", True, "", ms)
        except (ValueError, AttributeError):
            self.add(model, "json", False, f"content not JSON: {str(raw[:120])!r}", ms)


def _route_headers(headers: dict[str, str]) -> str:
    keep = {k: v for k, v in headers.items()
            if any(s in k for s in ("provider", "upstream", "route", "model", "served", "fallback"))}
    return ", ".join(f"{k}={v}" for k, v in keep.items())


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--base-url", default=os.environ.get("HANZO_BASE_URL", "https://api.hanzo.ai/v1"))
    ap.add_argument("--models", help="comma-separated ids to probe (default: from the catalog)")
    ap.add_argument("--limit", type=int, default=5, help="probe the first N catalog models (default 5)")
    ap.add_argument("--all", action="store_true", help="probe every catalog model (billed!)")
    ap.add_argument("--catalog-only", action="store_true", help="only GET /models; needs no key")
    ap.add_argument("--features", action="store_true", help="also check tools and response_format")
    ap.add_argument("--no-stream", action="store_true")
    ap.add_argument("--max-tokens", type=int, default=16)
    ap.add_argument("--timeout", type=float, default=60.0)
    ap.add_argument("--out", default="traces-137.jsonl", help="JSONL trace file")
    a = ap.parse_args()

    key = os.environ.get("HANZO_API_KEY")
    if not key and not a.catalog_only:
        print("HANZO_API_KEY is not set. Set it in your shell, or use --catalog-only.", file=sys.stderr)
        return 2

    with open(a.out, "w", encoding="utf-8") as trace:
        p = Probe(a.base_url.rstrip("/"), key, a.timeout, trace)
        print(f"probing {p.base}  (traces -> {a.out})")
        ids = p.catalog()
        if not a.catalog_only:
            p.unknown()
            models = a.models.split(",") if a.models else (ids if a.all else ids[: a.limit])
            for m in models:
                m = m.strip()
                if not m:
                    continue
                p.buffered(m, a.max_tokens)
                if not a.no_stream:
                    p.streamed(m, a.max_tokens)
                if a.features:
                    p.tools(m)
                    p.json_mode(m)

    failed = [r for r in p.results if not r.ok]
    print(f"\n{len(p.results) - len(failed)} passed, {len(failed)} failed")
    summary = a.out.rsplit(".", 1)[0] + "-summary.json"
    with open(summary, "w", encoding="utf-8") as f:
        json.dump([r.__dict__ for r in p.results], f, indent=1)
    print(f"summary -> {summary}")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
