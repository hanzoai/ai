// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package routers

import (
	"bufio"
	"bytes"
	stdcontext "context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/hanzoai/ai/internal/authtest"
	"github.com/hanzoai/ai/object"
	"github.com/zap-proto/zip"
)

type refusal struct {
	Error struct {
		Message    string
		Code       string
		Limit      string
		ResetsAt   string `json:"resets_at"`
		UpgradeURL string `json:"upgrade_url"`
	}
}

func refusalOf(t *testing.T, body string) refusal {
	t.Helper()
	var r refusal
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("refusal is not JSON: %s", body)
	}
	return r
}

// gateWith installs a test gate whose caller "tok" is acme/ann paying from acme, with
// the given wallet balance in cents.
func gateWith(t *testing.T, cents int64) {
	t.Helper()
	bg := newTestGate("http://unused", "", balanceCacheTTL)
	bg.setUserKeyCache("tok", "", "acme", "acme", "acme/ann")
	bg.ledger.SetBalance("acme", cents)
	prev := balanceGate
	balanceGate = bg
	t.Cleanup(func() { balanceGate = prev })
	t.Cleanup(func() { object.SetLimits(nil); object.SetSpent(nil) })
}

func chatWith(model, path string) probe {
	return ask(http.MethodPost, path).
		with("Authorization", "Bearer tok").
		body([]byte(`{"model":"` + model + `","max_tokens":1000,"messages":[{"role":"user","content":"hi"}]}`)).
		through(BalanceGateFilter)
}

// freeModels makes the named models cost nothing for the test's length.
func freeModels(t *testing.T, ids ...string) {
	t.Helper()
	prev := costsNothing
	costsNothing = func(model, org string) bool {
		for _, id := range ids {
			if model == id {
				return true
			}
		}
		return prev(model, org)
	}
	t.Cleanup(func() { costsNothing = prev })
}

// A spent window refuses the free lane too: limited mode answers from free models,
// and the plan's request windows are what keep that lane from being farmed. The free
// allowance is never reached for a caller whose plan said no.
func TestASpentWindowRefusesTheFreeLaneToo(t *testing.T) {
	gateWith(t, 0)
	freeModels(t, "enso")
	reset := time.Now().Add(90 * time.Minute).UTC().Truncate(time.Second)
	object.SetLimits(func(stdcontext.Context, object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
		return nil, &object.LimitHit{Code: object.CodeUsageCap, Name: "session", ResetsAt: reset, Upgrade: "max-20x"}, nil
	})
	read := 0
	object.SetSpent(func(stdcontext.Context, string, string) (object.Standing, error) {
		read++
		return object.Standing{Window: "day", Limit: 50}, nil
	})
	p := chatWith("enso", "/v1/chat/completions")
	if p.status() != http.StatusTooManyRequests || refusalOf(t, p.said()).Error.Code != object.CodeUsageCap || read != 0 {
		t.Fatalf("spent window on a free model: status %d, allowance read %d (%s), want the window's 429", p.status(), read, p.said())
	}
}

// Off a chat endpoint the plan is never asked: plans count chat requests only.
func TestThePlanCountsChatRequestsOnly(t *testing.T) {
	gateWith(t, 100000)
	asked := 0
	object.SetLimits(func(stdcontext.Context, object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
		asked++
		return nil, nil, nil
	})
	chatWith("zen-embedding", "/v1/embeddings")
	chatWith("enso", "/v1/images/generations")
	if asked != 0 {
		t.Fatalf("the plan was asked %d time(s) off a chat endpoint", asked)
	}
	chatWith("enso", "/V1/Chat/Completions/")
	if asked != 1 {
		t.Fatalf("a chat endpoint spelled another way was not asked (%d)", asked)
	}
}

// A spent window on a priced model refuses with 429 usage_cap_exceeded naming the
// window, its reset, Retry-After and the plan that raises it — never credit.
func TestASpentWindowIs429WithTheUpgrade(t *testing.T) {
	gateWith(t, 0)
	reset := time.Now().Add(90 * time.Minute).UTC().Truncate(time.Second)
	object.SetLimits(func(_ stdcontext.Context, q object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
		return nil, &object.LimitHit{Name: "session", ResetsAt: reset, Upgrade: "max-20x"}, nil
	})
	p := chatWith("enso", "/v1/chat/completions")
	if p.status() != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429 (%s)", p.status(), p.said())
	}
	r := refusalOf(t, p.said())
	if r.Error.Code != "usage_cap_exceeded" || r.Error.Limit != "session" || r.Error.ResetsAt != reset.Format(time.RFC3339) {
		t.Errorf("refusal %+v", r.Error)
	}
	if !strings.HasSuffix(r.Error.UpgradeURL, "/cart?plan=max-20x") {
		t.Errorf("upgrade_url %q, want the next plan's cart", r.Error.UpgradeURL)
	}
	if strings.Contains(strings.ToLower(r.Error.Message), "credit") {
		t.Errorf("a plan's refusal points at credit: %q", r.Error.Message)
	}
	if p.replied("Retry-After") == "" {
		t.Error("429 carries no Retry-After")
	}

	// At the top plan there is no upgrade to offer.
	object.SetLimits(func(stdcontext.Context, object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
		return nil, &object.LimitHit{Name: "day", ResetsAt: reset}, nil
	})
	if r := refusalOf(t, chatWith("enso", "/v1/chat/completions").said()); r.Error.Limit != "day" || r.Error.UpgradeURL != "" {
		t.Errorf("top plan's day refusal %+v", r.Error)
	}
}

// The policy is asked about every model on a chat endpoint, told its class, its
// Hanzo family, whether it is priced, the caller and whether it may reach paid
// upstream. Who pays is the host's answer for every class, not only Hanzo's SKUs.
func TestThePolicyIsAskedAboutEveryModelWithItsClass(t *testing.T) {
	gateWith(t, 100000)
	var asked []object.LimitAsk
	object.SetLimits(func(_ stdcontext.Context, q object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
		asked = append(asked, q)
		return nil, nil, nil
	})
	chatWith("enso-pro", "/v1/chat/completions")
	chatWith("zen5", "/v1/messages")
	chatWith("anthropic/claude-opus-5.5", "/v1/chat/completions")
	if len(asked) != 3 {
		t.Fatalf("asked %d times (%+v), want every model", len(asked), asked)
	}
	if asked[0].Family != "enso" || asked[0].Class != object.ClassOurs || !asked[0].Spend || asked[0].Subject != "acme" || asked[0].Namespace != "acme" || asked[0].Actor != "acme/ann" {
		t.Errorf("enso on chat asked %+v", asked[0])
	}
	if asked[1].Family != "zen" || asked[1].Class != object.ClassOurs || !asked[1].Spend {
		t.Errorf("zen on messages asked %+v", asked[1])
	}
	if asked[2].Family != "" || asked[2].Class != object.ClassPremium || !asked[2].Priced {
		t.Errorf("a third-party model asked %+v", asked[2])
	}
}

// A request the plan covers meets neither the free allowance nor the wallet: a $0
// balance and a spent allowance both admit it.
func TestACoveredRequestMeetsNoAllowanceAndNoWallet(t *testing.T) {
	gateWith(t, 0)
	read := 0
	object.SetSpent(func(stdcontext.Context, string, string) (object.Standing, error) {
		read++
		return object.Standing{Spent: true, Window: "day", Limit: 50}, nil
	})
	object.SetLimits(func(stdcontext.Context, object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
		return &object.LimitGrant{Plan: "max-20x", Pays: object.PaysPlan, Spend: 1, Settle: func(int64) {}}, nil, nil
	})
	if p := chatWith("enso", "/v1/chat/completions"); p.status() != http.StatusOK {
		t.Errorf("enso under a plan: status %d (%s)", p.status(), p.said())
	}
	if read != 0 {
		t.Errorf("the free allowance was read %d time(s) for a covered request", read)
	}

	// Without a plan the same request goes on to the gates below: here, priced in this
	// test's empty catalog, the wallet refuses it at $0.
	object.SetLimits(func(stdcontext.Context, object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
		return nil, nil, nil
	})
	if p := chatWith("enso", "/v1/chat/completions"); p.status() != http.StatusPaymentRequired {
		t.Errorf("no plan, $0: status %d, want the wallet's 402 (%s)", p.status(), p.said())
	}
}

// A plan that cannot be read covers nothing, and the request goes on as a free one:
// free AI keeps answering and nothing is held.
func TestAnUnreadablePlanServesAsFree(t *testing.T) {
	gateWith(t, 0)
	object.SetLimits(func(stdcontext.Context, object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
		return nil, nil, errors.New("store unreadable")
	})
	if p := chatWith("enso", "/v1/chat/completions"); p.status() == http.StatusServiceUnavailable || p.status() == http.StatusTooManyRequests {
		t.Errorf("unreadable plan: %d, want the request served as free (%s)", p.status(), p.said())
	}
}

// A priced call that names no model (crawl, ingest) is the balance gate's alone: the
// plan is never asked about it.
func TestPlanLimitsAreAskedOnlyForAModel(t *testing.T) {
	gateWith(t, 100000)
	asked := 0
	object.SetLimits(func(stdcontext.Context, object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
		asked++
		return nil, &object.LimitHit{Name: "day"}, nil
	})
	p := ask(http.MethodPost, "/v1/crawl").with("Authorization", "Bearer tok").body([]byte(`{"url":"https://example.com"}`)).through(BalanceGateFilter)
	if asked != 0 || p.status() == http.StatusTooManyRequests {
		t.Fatalf("a call naming no model asked the plan %d time(s), status %d", asked, p.status())
	}
}

// The gate reads the org the caller is working in: X-Org-Id when the signed `orgs`
// claim lists it, the home org (orgs[0]) when the header is absent, and nobody when
// it names an org the claim does not list. The plan is asked about exactly that org,
// with the apps the token was minted for.
func TestTheGateReadsTheOrgTheCallerIsWorkingIn(t *testing.T) {
	bg := newTestGate("http://unused", "", balanceCacheTTL)
	bg.ledger.SetBalance("webby-ai", 0)
	bg.ledger.SetBalance("joshuafl369", 0)
	prev := balanceGate
	balanceGate = bg
	t.Cleanup(func() { balanceGate = prev })
	t.Cleanup(func() { object.SetLimits(nil) })

	key := authtest.Signing(t)
	tok, err := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"owner": "hanzo", "name": "joshuafl369", "billing_account": "org:joshuafl369",
		"iss": "https://hanzo.id", "aud": "hanzo-app",
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
		"orgs": []map[string]string{{"org": "joshuafl369", "role": "admin"}, {"org": "webby-ai", "role": "owner"}},
	}).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}

	var asked []object.LimitAsk
	object.SetLimits(func(_ stdcontext.Context, q object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
		asked = append(asked, q)
		if q.Namespace == "webby-ai" {
			return &object.LimitGrant{Plan: "max-20x", Pays: object.PaysPlan, Settle: func(int64) {}}, nil, nil
		}
		return nil, &object.LimitHit{Name: "day", ResetsAt: time.Now().Add(time.Hour)}, nil
	})
	post := func(org string) int {
		p := ask(http.MethodPost, "/v1/chat/completions").with("Authorization", "Bearer "+tok)
		if org != "" {
			p = p.with("X-Org-Id", org)
		}
		return p.body([]byte(`{"model":"enso","messages":[]}`)).through(BalanceGateFilter).status()
	}

	if code := post("webby-ai"); code != http.StatusOK {
		t.Fatalf("working in the paid org: %d, want admitted", code)
	}
	if code := post(""); code != http.StatusTooManyRequests {
		t.Fatalf("working in the home org: %d, want its 429", code)
	}
	if len(asked) != 2 || asked[0].Namespace != "webby-ai" || asked[0].Subject != "webby-ai" ||
		asked[1].Namespace != "joshuafl369" || asked[1].Actor != "joshuafl369/joshuafl369" {
		t.Fatalf("limits asked %+v", asked)
	}
	if len(asked[0].Apps) != 1 || asked[0].Apps[0] != "hanzo-app" {
		t.Errorf("apps %v, want the token's audience", asked[0].Apps)
	}
	asked = nil
	post("acme") // an org the claim does not list
	if len(asked) != 0 {
		t.Fatalf("a non-member X-Org-Id reached the plan: %+v", asked)
	}
}

// A covered request whose answer was whole settles its grant at nothing once its
// handler is done, so a hold never outlives a request that ended without recording
// usage (a usage record settles it first, at what it cost). A streamed answer is
// settled by its own writer, never here.
func TestACoveredRequestSettlesWhenItsAnswerIsWhole(t *testing.T) {
	gateWith(t, 0)
	var settled []int64
	object.SetLimits(func(stdcontext.Context, object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
		return &object.LimitGrant{Plan: "max-20x", Pays: object.PaysPlan, Spend: 7, Settle: func(n int64) { settled = append(settled, n) }}, nil, nil
	})
	chatWith("enso", "/v1/chat/completions")
	if len(settled) != 1 || settled[0] != 0 {
		t.Fatalf("a whole answer settled %v, want [0]", settled)
	}

	settled = nil
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Use(zip.H(BalanceGateFilter))
	app.Raw(zip.MethodAll, "/*", func(c *zip.Ctx) error {
		return c.SendStreamWriter(func(w *bufio.Writer) { _, _ = w.WriteString("data: {}\n\n") })
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(`{"model":"enso","messages":[]}`)))
	req.Header.Set("Authorization", "Bearer tok")
	if _, err := app.Fiber().Test(req); err != nil {
		t.Fatal(err)
	}
	if len(settled) != 0 {
		t.Fatalf("the filter settled a streamed answer's grant: %v", settled)
	}
}
