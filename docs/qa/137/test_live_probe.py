#!/usr/bin/env python3
"""Self-test for live_probe.py: runs it against a local fake gateway whose models
each fail in one of the silent ways #137 names, and checks the probe catches every
one — and passes the one model that behaves.

    python test_live_probe.py
"""
from __future__ import annotations

import json
import os
import subprocess
import sys
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

HERE = os.path.dirname(os.path.abspath(__file__))

# model id -> (buffered behaviour, stream behaviour)
MODELS = {
    "good":          ("ok", "ok"),
    "empty-choices": ("empty", "ok"),
    "html-200":      ("html", "ok"),
    "substitutes":   ("wrong-model", "wrong-model"),
    "cut-stream":    ("ok", "cut"),
    "no-done":       ("ok", "no-done"),
    "error-frame":   ("ok", "error-frame"),
}


def completion(model, content="pong", finish="stop"):
    return {"id": "chatcmpl-x", "object": "chat.completion", "created": 1, "model": model,
            "choices": [{"index": 0, "message": {"role": "assistant", "content": content}, "finish_reason": finish}],
            "usage": {"prompt_tokens": 5, "completion_tokens": 1, "total_tokens": 6}}


def chunk(model, delta, finish=None):
    return "data: " + json.dumps({"id": "chatcmpl-x", "object": "chat.completion.chunk", "created": 1, "model": model,
                                   "choices": [{"index": 0, "delta": delta, "finish_reason": finish}]}) + "\n\n"


class Fake(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def _json(self, status, doc, ctype="application/json"):
        b = doc.encode() if isinstance(doc, str) else json.dumps(doc).encode()
        self.send_response(status)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(b)))
        self.end_headers()
        self.wfile.write(b)

    def do_GET(self):
        if self.path.endswith("/models"):
            self._json(200, {"object": "list", "data": [{"id": m, "object": "model"} for m in MODELS]})
        else:
            self._json(404, {"error": {"message": "nope"}})

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))) or b"{}")
        model = body.get("model", "")
        if model not in MODELS:
            # The failure the probe hunts: an unknown id answered by something else.
            return self._json(200, completion("good"))
        buffered, streamed = MODELS[model]
        if not body.get("stream"):
            if buffered == "empty":
                return self._json(200, {"id": "x", "object": "chat.completion", "model": model, "choices": []})
            if buffered == "html":
                return self._json(200, "<html><h1>502 Bad Gateway</h1></html>", "text/html")
            if buffered == "wrong-model":
                return self._json(200, completion("cheaper-model"))
            return self._json(200, completion(model))

        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        name = "cheaper-model" if streamed == "wrong-model" else model
        w = self.wfile.write
        w(chunk(name, {"role": "assistant", "content": ""}).encode())
        w(chunk(name, {"content": "po"}).encode())
        if streamed == "cut":
            self.wfile.flush()
            self.connection.shutdown(2)
            return
        if streamed == "error-frame":
            w(b'data: {"error":{"message":"upstream overloaded","code":502}}\n\n')
            return
        w(chunk(name, {"content": "ng"}).encode())
        if streamed == "no-done":
            return
        w(chunk(name, {}, "stop").encode())
        w(b"data: [DONE]\n\n")


def main() -> int:
    srv = ThreadingHTTPServer(("127.0.0.1", 0), Fake)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    base = f"http://127.0.0.1:{srv.server_address[1]}/v1"
    with tempfile.TemporaryDirectory() as tmp:
        out = os.path.join(tmp, "traces.jsonl")
        env = dict(os.environ, HANZO_API_KEY="fake-key-for-local-test")
        proc = subprocess.run([sys.executable, "-I", os.path.join(HERE, "live_probe.py"),
                               "--base-url", base, "--all", "--out", out, "--timeout", "10"],
                              env=env, capture_output=True, text=True)
        print(proc.stdout)
        if proc.stderr:
            print(proc.stderr, file=sys.stderr)
        results = json.load(open(out.rsplit(".", 1)[0] + "-summary.json"))
        traces = open(out, encoding="utf-8").read()
    srv.shutdown()

    got = {(r["model"], r["check"]): r["ok"] for r in results}
    want_fail = {("empty-choices", "buffered"), ("html-200", "buffered"), ("substitutes", "buffered"),
                 ("substitutes", "stream"), ("cut-stream", "stream"), ("no-done", "stream"),
                 ("error-frame", "stream")}
    bad = []
    for (model, check), ok in got.items():
        if model.startswith("qa-137-no-such-model"):
            if ok:
                bad.append("the unknown-model substitution was not caught")
            continue
        if (model, check) in want_fail and ok:
            bad.append(f"{model}/{check}: a silent failure passed")
        if (model, check) not in want_fail and not ok:
            bad.append(f"{model}/{check}: a good answer was flagged")
    for pair in want_fail:
        if pair not in got:
            bad.append(f"{pair}: never checked")
    if "fake-key-for-local-test" in traces:
        bad.append("the API key leaked into the trace file")
    if proc.returncode != 1:
        bad.append(f"probe exit code {proc.returncode}, want 1 when anything failed")

    if bad:
        print("SELF-TEST FAILED:\n  " + "\n  ".join(bad))
        return 1
    print(f"self-test passed: {len(want_fail) + 1} silent failures caught, good model passed, key redacted")
    return 0


if __name__ == "__main__":
    sys.exit(main())
