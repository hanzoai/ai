// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/internal/stub"
	"github.com/hanzoai/ai/plugin"
)

// inproc registers the stub plugin as name, relaying to upstream with key as the
// value of the key it names, and returns its address.
func inproc(name, upstream, key string) string {
	plugin.Register(plugin.Plugin{
		Name:   name,
		Config: []byte(upstream),
		New:    func(h *plugin.Host) plugin.Guest { return stub.New(plugin.Imports[*stub.Module]{H: h}) },
		Keys: func(n string) string {
			if n == "STUB_KEY" {
				return key
			}
			return ""
		},
	})
	return plugin.Scheme + "://" + name
}

// A family answered in process is the family answered over HTTP: discovered,
// routed, relayed, billed exactly, costed and edited the same, because the
// address is the only thing that changed.
func TestAFamilyInProcessIsServedAndBilledAsOverHTTP(t *testing.T) {
	z := zenServiceAt(t, true)
	t.Setenv("ZEN_URL", inproc("zen-inproc", z.URL, zenServiceToken))
	zenFam.mu.Lock()
	zenFam.byID, zenFam.ids, zenFam.named, zenFam.paid, zenFam.loaded = nil, nil, nil, false, false
	zenFam.mu.Unlock()
	if err := zenFam.refresh(); err != nil {
		t.Fatalf("discovery in process: %v", err)
	}
	if !zenFam.open() || zenFam.sku("claude-zen") != "zen6" {
		t.Fatalf("discovery in process read paid=%v, claude-zen -> %q", zenFam.open(), zenFam.sku("claude-zen"))
	}

	got := ledger(t)
	alice := &iam.User{Owner: "acme", Name: "alice"}

	body := []byte(`{"model":"zen5-pro","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	c := visit(http.MethodPost, "/v1/chat/completions")
	c.Fiber().Request().SetBody(body)
	if out := c.pipeToFamily(zenFam, "chat/completions", "openai", "zen5-pro", body, true, 0, "acme", alice, true, nil, time.Now()); out != nil {
		t.Fatalf("refused: %+v", out)
	}
	if s := drain(t, c); !strings.Contains(s, `"content":"ok"`) {
		t.Errorf("the stream relayed %q", s)
	}
	if ev := got(); len(ev) != 1 || ev[0].USD != "0.45" || ev[0].CostUSD != "0.000123" {
		t.Fatalf("the stream debited %+v, want $0.45 at the plugin's COGS of $0.000123", ev)
	}

	emb := []byte(`{"model":"zen-embedding","input":"hello"}`)
	c = visit(http.MethodPost, "/v1/embeddings")
	c.Fiber().Request().SetBody(emb)
	if out := c.pipeToFamily(zenFam, "embeddings", "openai", "zen-embedding", emb, false, 0, "acme", alice, true, nil, time.Now()); out != nil {
		t.Fatalf("refused: %+v", out)
	}
	if ev := got(); len(ev) != 2 || ev[1].USD != "0.00001" {
		t.Fatalf("an embedding debited %+v, want $0.00001", ev)
	}

	rr := []byte(`{"model":"zen-rerank","query":"q","documents":["a"]}`)
	c = visit(http.MethodPost, "/v1/rerank")
	c.serveZenMedia("rerank", "zen-rerank", rr, 1, "acme", alice, true, time.Now())
	if answered(c) != http.StatusOK {
		t.Fatalf("rerank answered %d %s", answered(c), sent(c))
	}
	if ev := got(); len(ev) != 3 || ev[2].USD != "0.001" || ev[2].CostUSD != "0.000123" {
		t.Fatalf("a rerank debited %+v, want $0.001 at the plugin's COGS", ev)
	}

	Zen = nil
	h, err := routingHost("zen")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Apply(context.Background(), []byte(`{"models":{"zen6":{"route":[{"upstream":"b:free"}]}}}`)); err != nil {
		t.Fatalf("apply in process: %v", err)
	}
	if snap, err := h.Snapshot(context.Background()); err != nil || !strings.Contains(string(snap), "b:free") {
		t.Fatalf("snapshot in process %s %v", snap, err)
	}
}

// A decision answered in process reaches Kai's service as over HTTP and comes
// back the same, billed once.
func TestADecisionInProcessIsAnsweredAndBilledAsOverHTTP(t *testing.T) {
	fake, events := setupDecisions(t)
	t.Setenv("KAI_URL", inproc("kai-inproc", os.Getenv("KAI_URL"), "k"))

	status, body := driveDecisions(t, "Bearer "+decisionsKey, decisionBody)
	if status != http.StatusOK || body != decisionAnswer {
		t.Fatalf("in process answered %d %s", status, body)
	}
	if calls, path, sentBody := fake.seen(); calls != 1 || path != "/v1/decisions" || !strings.Contains(string(sentBody), `"questions"`) {
		t.Errorf("Kai's service saw %d calls at %s: %s", calls, path, sentBody)
	}
	settled(t)
	if len(*events) != 1 {
		t.Errorf("billed %d times, want once", len(*events))
	}
}
