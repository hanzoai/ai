// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package plugin

import (
	"bytes"
	"io"
	"net/http"
	"slices"
	"testing"
)

// A request's identity and decision fields cross as the Ask's typed fields, the
// rest as headers, and no credential crosses at all.
func TestARequestBecomesItsAsk(t *testing.T) {
	r, _ := http.NewRequest(http.MethodPost, "plugin://kai/v1/decisions?x=1", nil)
	r.Header.Set("Authorization", "Bearer service-key")
	r.Header.Set("X-Org-Id", "acme")
	r.Header.Set("X-Request-Id", "req_1")
	r.Header.Set("X-Org-Capabilities", `[{"name":"queue"}]`)
	r.Header.Set("X-Capture", "1")
	r.Header.Set("X-Hanzo-Spend", "0.50")
	r.Header.Set("X-Hanzo-Fronted-By", "ai")
	r.Header.Set("Anthropic-Version", "2023-06-01")
	a := ask(r)
	if a.Method != "POST" || a.Path != "/v1/decisions?x=1" || a.Org != "acme" || a.RequestID != "req_1" ||
		string(a.Capabilities) != `[{"name":"queue"}]` || !a.Capture || a.Spend != "0.50" || !a.Fronted {
		t.Errorf("ask %+v", a)
	}
	if !slices.Equal(a.Headers, [][2]string{{"Anthropic-Version", "2023-06-01"}}) {
		t.Errorf("headers %v, want only what has no field and no credential", a.Headers)
	}
}

// Tells are the X-Hanzo-* fields a family service sends, one Failover field an
// arm, and End arrives as the trailers once the body has been read.
func TestAnAnswerStatesItsTellsAsHeadersAndItsEndAsTrailers(t *testing.T) {
	h := http.Header{}
	tell(h, Tells{Served: "zen6", Arm: "a/b", Provider: "p", Free: true, Failover: []string{"x (y): z", "u (v): w"}})
	if h.Get(headerServed) != "zen6" || h.Get(headerArm) != "a/b" || h.Get(headerProvider) != "p" || h.Get(headerFree) != "true" || len(h.Values(headerFailover)) != 2 {
		t.Errorf("tells %v", h)
	}
	resp := &http.Response{}
	tr := &trailed{ReadCloser: io.NopCloser(bytes.NewReader([]byte("ok"))), resp: resp, end: func() End {
		return End{Cogs: "0.000002", Cost: "0.0025", Tells: Tells{Arm: "c/d"}}
	}}
	_, _ = io.ReadAll(tr)
	if resp.Trailer.Get(headerCogs) != "0.000002" || resp.Trailer.Get(headerCost) != "0.0025" || resp.Trailer.Get(headerArm) != "c/d" {
		t.Errorf("trailers %v", resp.Trailer)
	}
}

// fakeWASI records how Sandbox configured it.
type fakeWASI struct {
	env, args      []string
	stdin          io.Reader
	stdout, stderr io.Writer
	fs             func(string, bool) bool
	net            func(string) bool
	dial           func(string, string, string, int) bool
	resolve        func(string) bool
	exec           func(string, []string) bool
}

func (f *fakeWASI) SetEnv(e []string)                                    { f.env = e }
func (f *fakeWASI) SetArgs(a []string)                                   { f.args = a }
func (f *fakeWASI) SetStdin(r io.Reader)                                 { f.stdin = r }
func (f *fakeWASI) SetStdout(w io.Writer)                                { f.stdout = w }
func (f *fakeWASI) SetStderr(w io.Writer)                                { f.stderr = w }
func (f *fakeWASI) SetFSAccessHook(h func(string, bool) bool)            { f.fs = h }
func (f *fakeWASI) SetNetAccessHook(h func(string) bool)                 { f.net = h }
func (f *fakeWASI) SetDialHook(h func(string, string, string, int) bool) { f.dial = h }
func (f *fakeWASI) SetResolveHook(h func(string) bool)                   { f.resolve = h }
func (f *fakeWASI) SetExecHook(h func(string, []string) bool)            { f.exec = h }

// A sandboxed plugin sees no environment (where the keys are), opens no file,
// socket or process, and its output is the host's log.
func TestASandboxedPluginReachesNothingButTheHost(t *testing.T) {
	w := Sandbox(&fakeWASI{env: []string{"OPENROUTER_API_KEY=sk"}}, "zen")
	if len(w.env) != 0 || !slices.Equal(w.args, []string{"zen"}) || w.stdout == nil || w.stderr == nil || w.stdin == nil {
		t.Errorf("sandbox %+v", w)
	}
	if w.fs("catalog.yaml", false) || w.net("send") || w.dial("tcp", "api.openai.com", "1.2.3.4", 443) || w.resolve("api.openai.com") || w.exec("/bin/sh", nil) {
		t.Error("a sandboxed plugin reached past the host")
	}
}
