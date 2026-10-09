// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/plugin"
)

// A family answered in process is the family answered over HTTP: discovered,
// routed, relayed, billed exactly, costed and edited the same, because the
// address is the only thing that changed. The same service answers both ways —
// once at its network address, once registered in this process.
func TestAFamilyInProcessIsServedAndBilledAsOverHTTP(t *testing.T) {
	z := zenServiceAt(t, true)
	plugin.Register("zen-inproc", http.HandlerFunc(z.serve))
	alice := &iam.User{Owner: "acme", Name: "alice"}

	// serve runs the family's three paths at address and answers what each
	// relayed and debited.
	serve := func(address string) ([]string, []object.UsageEvent) {
		t.Setenv("ZEN_URL", address)
		zenFam.mu.Lock()
		zenFam.byID, zenFam.ids, zenFam.named, zenFam.paid, zenFam.loaded = nil, nil, nil, false, false
		zenFam.mu.Unlock()
		if err := zenFam.refresh(); err != nil {
			t.Fatalf("discovery at %s: %v", address, err)
		}
		if !zenFam.open() || zenFam.sku("claude-zen") != "zen6" {
			t.Fatalf("discovery at %s read paid=%v, claude-zen -> %q", address, zenFam.open(), zenFam.sku("claude-zen"))
		}
		got := ledger(t)
		var bodies []string

		chat := []byte(`{"model":"zen5-pro","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
		c := visit(http.MethodPost, "/v1/chat/completions")
		c.Fiber().Request().SetBody(chat)
		if out := c.pipeToFamily(zenFam, "chat/completions", "openai", "zen5-pro", chat, true, 0, "acme", alice, true, nil, time.Now()); out != nil {
			t.Fatalf("%s refused a stream: %+v", address, out)
		}
		bodies = append(bodies, drain(t, c))

		emb := []byte(`{"model":"zen-embedding","input":"hello"}`)
		c = visit(http.MethodPost, "/v1/embeddings")
		c.Fiber().Request().SetBody(emb)
		if out := c.pipeToFamily(zenFam, "embeddings", "openai", "zen-embedding", emb, false, 0, "acme", alice, true, nil, time.Now()); out != nil {
			t.Fatalf("%s refused an embedding: %+v", address, out)
		}
		bodies = append(bodies, sent(c))

		rr := []byte(`{"model":"zen-rerank","query":"q","documents":["a"]}`)
		c = visit(http.MethodPost, "/v1/rerank")
		c.serveZenMedia("rerank", "zen-rerank", rr, 1, "acme", alice, true, time.Now())
		if answered(c) != http.StatusOK {
			t.Fatalf("%s answered a rerank %d %s", address, answered(c), sent(c))
		}
		bodies = append(bodies, sent(c))
		return bodies, got()
	}

	overHTTP, billedHTTP := serve(z.URL)
	inProcess, billedHere := serve(plugin.Scheme + "://zen-inproc")
	// Each answer's id is minted here per call; everything else is the family's.
	minted := regexp.MustCompile(`chatcmpl-[0-9a-f-]+`)
	same := func(bodies []string) string { return minted.ReplaceAllString(strings.Join(bodies, "\n"), "chatcmpl-") }
	if same(inProcess) != same(overHTTP) {
		t.Errorf("in process relayed\n%v\nover HTTP\n%v", inProcess, overHTTP)
	}
	if !strings.Contains(inProcess[0], `"content":"ok"`) {
		t.Errorf("the stream relayed %q", inProcess[0])
	}
	if len(billedHere) != 3 || len(billedHTTP) != 3 {
		t.Fatalf("debited %d in process and %d over HTTP, want 3 each", len(billedHere), len(billedHTTP))
	}
	for i := range billedHere {
		a, b := billedHere[i], billedHTTP[i]
		if a.USD != b.USD || a.CostUSD != b.CostUSD || a.Units != b.Units || a.Model != b.Model || a.Namespace != b.Namespace {
			t.Errorf("debit %d in process %+v, over HTTP %+v", i, a, b)
		}
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
	plugin.Register("kai-inproc", fake)
	t.Setenv("KAI_URL", plugin.Scheme+"://kai-inproc")

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
