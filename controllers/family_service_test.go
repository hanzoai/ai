// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
)

// shipped is the paid switch as the package ships it, read before TestMain pins it
// open: package variables initialize before TestMain runs.
var shipped = FreeOnly

// zenService is a zen family service on httptest, serving the contract ai reads from
// the family it fronts: discovery with the catalog's aliases and paid line; chat, whole
// and streamed, with the X-Hanzo-* records and the request's COGS (a header on a whole
// answer, a trailer on a stream); embeddings; rerank; and the admin catalog behind
// ZEN_ADMIN_TOKEN.
type zenService struct {
	*httptest.Server
	token string

	mu      sync.Mutex
	paid    bool
	catalog string
	asked   []string // the model each inference call named
	orgs    []string // the X-Org-Id each inference call carried
}

const zenServiceToken = "admin-token"

// zenServiceAt starts a zen service whose catalog says paid, points the zen family at
// it, and discovers it once.
func zenServiceAt(t *testing.T, paid bool) *zenService {
	t.Helper()
	cooled.forget()
	t.Cleanup(cooled.forget)
	z := &zenService{token: zenServiceToken, paid: paid, catalog: `{"models":{"zen6":{"route":[{"upstream":"a:free"}]}}}`}
	z.Server = httptest.NewServer(http.HandlerFunc(z.serve))
	t.Cleanup(z.Close)
	t.Setenv("ZEN_URL", z.URL)
	t.Setenv("ZEN_API_KEY", "")
	t.Setenv("ZEN_ADMIN_TOKEN", zenServiceToken)
	restore(t, zenFam)
	zenFam.mu.Lock()
	zenFam.byID, zenFam.ids, zenFam.spares, zenFam.named, zenFam.paid = nil, nil, nil, nil, false
	zenFam.loaded, zenFam.fetchedAt = false, time.Time{}
	zenFam.mu.Unlock()
	if err := zenFam.refresh(); err != nil {
		t.Fatalf("discovery: %v", err)
	}
	return z
}

func (z *zenService) serve(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/v1/admin/zen/") {
		z.admin(w, r)
		return
	}
	if r.URL.Path == "/v1/models" {
		z.mu.Lock()
		paid := z.paid
		z.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"object":"list","paid":%t,
"aliases":{"claude-zen":"zen6","hanzoai/zen":"hanzo/zen","zen6":"zen5-pro"},
"data":[
 {"id":"zen6","object":"model","owned_by":"zenlm","mode":"chat","context_window":262144,"pricing":{"input":"0","output":"0"}},
 {"id":"hanzo/zen","object":"model","owned_by":"hanzo","mode":"chat","pricing":{"input":"0","output":"0"}},
 {"id":"zen5-pro","object":"model","owned_by":"zenlm","mode":"chat","context_window":1000000,"funding":"prepaid","pricing":{"input":"3","output":"15"}},
 {"id":"zen-embedding","object":"model","owned_by":"zenlm","mode":"embedding","context_window":8192,"pricing":{"input":"0.01","output":"0.01"}},
 {"id":"zen-embedding-cash","object":"model","owned_by":"zenlm","mode":"embedding","funding":"prepaid","pricing":{"input":"0.05","output":"0.05"}},
 {"id":"zen-rerank","object":"model","owned_by":"zenlm","mode":"rerank","pricing":{"input":"0.001","output":"0"}}
]}`, paid)
		return
	}
	var in struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	b, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(b, &in)
	z.mu.Lock()
	z.asked = append(z.asked, in.Model)
	z.orgs = append(z.orgs, r.Header.Get("X-Org-Id"))
	z.mu.Unlock()
	if r.Header.Get("X-Hanzo-Fronted-By") == "ai" {
		w.Header().Set(armHeader, "vendor/glm")
		w.Header().Set(providerHeader, "router")
		w.Header().Add(failoverHeader, "vendor/a (free): upstream status 429")
		w.Header().Add(failoverHeader, "vendor/b (free): empty answer; retried once")
	}
	switch r.URL.Path {
	case "/v1/chat/completions":
		if in.Stream {
			w.Header().Set("Trailer", cogsHeader)
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set(servedHeader, in.Model)
			fmt.Fprintf(w, "data: {\"id\":\"g\",\"object\":\"chat.completion.chunk\",\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n", in.Model)
			fmt.Fprintf(w, "data: {\"id\":\"g\",\"object\":\"chat.completion.chunk\",\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":100000,\"completion_tokens\":10000,\"total_tokens\":110000}}\n\n", in.Model)
			fmt.Fprint(w, "data: [DONE]\n\n")
			w.Header().Set(cogsHeader, "0.000123")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(cogsHeader, "0.000123")
		w.Header().Set(servedHeader, in.Model)
		fmt.Fprintf(w, `{"id":"g","object":"chat.completion","model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`, in.Model)
	case "/v1/embeddings":
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(cogsHeader, "0.000002")
		fmt.Fprintf(w, `{"object":"list","model":%q,"data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"usage":{"prompt_tokens":1000,"total_tokens":1000}}`, in.Model)
	case "/v1/rerank":
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(cogsHeader, "0.0001")
		fmt.Fprintf(w, `{"object":"list","model":%q,"results":[{"index":0,"relevance_score":0.9}]}`, in.Model)
	default:
		http.NotFound(w, r)
	}
}

// admin is the family's admin surface, behind its token.
func (z *zenService) admin(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+z.token {
		http.Error(w, `{"error":{"message":"unauthorized"}}`, http.StatusUnauthorized)
		return
	}
	z.mu.Lock()
	defer z.mu.Unlock()
	switch r.Method + " " + r.URL.Path {
	case "GET /v1/admin/zen/catalog":
		_, _ = io.WriteString(w, z.catalog)
	case "PUT /v1/admin/zen/catalog":
		b, _ := io.ReadAll(r.Body)
		z.catalog = string(b)
		_, _ = io.WriteString(w, `{"ok":true}`)
	case "GET /v1/admin/zen/keys":
		_, _ = io.WriteString(w, `{"keys":[{"name":"ZEN_KEY","left":12.5}]}`)
	case "GET /v1/admin/zen/free":
		fmt.Fprintf(w, `{"paid":%t,"lanes":{}}`, z.paid)
	default:
		http.NotFound(w, r)
	}
}

func (z *zenService) heard() (asked, orgs []string) {
	z.mu.Lock()
	defer z.mu.Unlock()
	return append([]string(nil), z.asked...), append([]string(nil), z.orgs...)
}

// ledger captures every debit ai files, whole.
func ledger(t *testing.T) func() []object.UsageEvent {
	t.Helper()
	prev := object.UsageRecorder()
	var mu sync.Mutex
	var got []object.UsageEvent
	object.SetUsageRecorder(func(_ context.Context, u object.UsageEvent) error {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, u)
		return nil
	})
	t.Cleanup(func() { object.SetUsageRecorder(prev) })
	return func() []object.UsageEvent {
		mu.Lock()
		defer mu.Unlock()
		return append([]object.UsageEvent(nil), got...)
	}
}

// unseated is c with no seat on the paid lane: a caller no paid plan stands behind.
func unseated(c *ApiController) *ApiController {
	c.SetContext(context.WithValue(c.Context(), laneKey{}, &laneState{}))
	return c
}

// The paid lane is the zen catalog's `paid` line, read from the family's own
// discovery: open where the catalog says paid, closed where it does not, closed
// before the catalog is ever read, and unmoved by a refresh that fails. Nothing has
// to set it.
func TestThePaidSwitchIsTheZenCatalogsLine(t *testing.T) {
	z := zenServiceAt(t, true)
	FreeOnly = shipped
	t.Cleanup(func() { FreeOnly = func() bool { return false } })
	if FreeOnly() {
		t.Fatal("a catalog that says paid left the paid lane closed")
	}
	// With the switch the catalog's, a third-party provider is served to a caller the
	// lane seats, as it is where the catalog's own zen set it.
	if paying(paidSeat(context.Background()), &object.Provider{Type: "OpenAI"}) {
		t.Error("a priced provider was refused on a paid lane the catalog opened")
	}

	z.mu.Lock()
	z.paid = false
	z.mu.Unlock()
	if err := zenFam.refresh(); err != nil {
		t.Fatal(err)
	}
	if !FreeOnly() {
		t.Fatal("a catalog that stopped saying paid left the paid lane open")
	}
	if !paying(paidSeat(context.Background()), &object.Provider{Type: "OpenAI"}) {
		t.Error("a priced provider was served with the paid lane closed")
	}

	z.mu.Lock()
	z.paid = true
	z.mu.Unlock()
	_ = zenFam.refresh()
	z.Close()
	if err := zenFam.refresh(); err == nil {
		t.Fatal("a closed service refreshed")
	}
	if FreeOnly() {
		t.Error("a failed refresh closed a lane the last catalog opened")
	}

	zenFam.mu.Lock()
	zenFam.paid, zenFam.loaded = false, false
	zenFam.mu.Unlock()
	if !FreeOnly() {
		t.Error("a catalog never read opened the paid lane")
	}
}

// An embedding the family funds from grants and our own compute on every path is
// served to a caller the paid lane does not seat: a closed lane bounds the
// platform's cash, and the call spends none. One the catalog funds from a prepaid
// balance is still refused, since nothing stands in for an embedding.
func TestAGrantFundedEmbeddingIsServedOffThePaidLane(t *testing.T) {
	z := zenServiceAt(t, true)
	FreeOnly = shipped
	t.Cleanup(func() { FreeOnly = func() bool { return false } })
	alice := &iam.User{Owner: "acme", Name: "alice"}

	body := []byte(`{"model":"zen-embedding","input":"hello"}`)
	c := unseated(visit(http.MethodPost, "/v1/embeddings"))
	c.Fiber().Request().SetBody(body)
	if !FreeOnlyFor(c.Context()) {
		t.Fatal("want a caller the paid lane does not seat")
	}
	if out := c.pipeToFamily(zenFam, "embeddings", "openai", "zen-embedding", body, false, 0, "acme", alice, true, nil, time.Now()); out != nil {
		t.Fatalf("zen-embedding was refused off the paid lane: %+v", out)
	}
	if got := answered(c); got != http.StatusOK || !strings.Contains(sent(c), `"embedding"`) {
		t.Fatalf("zen-embedding answered %d %s", got, sent(c))
	}

	cash := []byte(`{"model":"zen-embedding-cash","input":"hello"}`)
	c = unseated(visit(http.MethodPost, "/v1/embeddings"))
	c.Fiber().Request().SetBody(cash)
	if out := c.pipeToFamily(zenFam, "embeddings", "openai", "zen-embedding-cash", cash, false, 0, "acme", alice, true, nil, time.Now()); out == nil {
		t.Fatal("an embedding that spends a prepaid balance was served to a caller the paid lane does not seat")
	}
	if asked, _ := z.heard(); len(asked) != 1 || asked[0] != "zen-embedding" {
		t.Errorf("the family was asked %v, want only zen-embedding", asked)
	}
}

// With no zen linked into this process, the routing catalog a SuperAdmin edits is
// the zen service's, over its admin routes and token: read, applied, its standing
// read back, and a model probed through the service itself.
func TestTheZenCatalogIsTheServicesWhenNoZenIsLinked(t *testing.T) {
	z := zenServiceAt(t, true)
	Zen = nil

	h, err := routingHost("zen")
	if err != nil {
		t.Fatalf("routingHost(zen) = %v, want the service at ZEN_URL", err)
	}
	ctx := context.Background()
	if snap, err := h.Snapshot(ctx); err != nil || !strings.Contains(string(snap), "a:free") {
		t.Fatalf("snapshot %s %v", snap, err)
	}
	if err := h.Apply(ctx, []byte(`{"models":{"zen6":{"route":[{"upstream":"b:free"}]}}}`)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	z.mu.Lock()
	applied := z.catalog
	z.mu.Unlock()
	if !strings.Contains(applied, "b:free") {
		t.Fatalf("the service holds %s after the apply", applied)
	}
	if stats, err := h.Stats(ctx); err != nil || !strings.Contains(string(stats), "ZEN_KEY") || !strings.Contains(string(stats), `"paid":true`) {
		t.Fatalf("stats %s %v", stats, err)
	}
	if p, err := routingTest(ctx, "zen", "zen6"); err != nil || p.Status != http.StatusOK || p.SKU != "zen6" || p.Arm != "vendor/glm" {
		t.Fatalf("probe %+v %v", p, err)
	}

	t.Setenv("ZEN_ADMIN_TOKEN", "wrong")
	if _, err := h.Snapshot(ctx); err == nil {
		t.Error("the service answered a token it does not hold")
	}
	t.Setenv("ZEN_URL", "")
	if _, err := routingHost("zen"); err == nil {
		t.Error("a zen that is neither linked nor configured resolved a catalog")
	}
}

// An alias the zen catalog states routes to the zen family as the SKU it names: the
// family is sent the SKU, the answer wears the alias, it is priced as the SKU, and it
// is not listed beside it. An alias that spells a SKU the catalog lists is that SKU.
func TestTheZenCatalogsAliasesReachTheFamily(t *testing.T) {
	z := zenServiceAt(t, true)
	prev := globalModelConfig
	globalModelConfig = nil
	t.Cleanup(func() { globalModelConfig = prev })

	for alias, sku := range map[string]string{"claude-zen": "zen6", "hanzoai/zen": "hanzo/zen", "CLAUDE-ZEN": "zen6"} {
		r := resolveModelRoute(alias)
		if r == nil || r.providerName != "zen" || r.upstreamModel != sku {
			t.Fatalf("%s routes to %+v, want zen/%s", alias, r, sku)
		}
		if FamilyOf(alias) != "zen" {
			t.Errorf("FamilyOf(%s) = %q, want zen", alias, FamilyOf(alias))
		}
	}
	if got := zenFam.sku("zen6"); got != "zen6" {
		t.Errorf("an alias spelling the listed SKU zen6 sent %q upstream", got)
	}
	for _, m := range mergeFamilyModels(nil) {
		if m.ID == "claude-zen" || m.ID == "hanzoai/zen" {
			t.Errorf("alias %s is listed", m.ID)
		}
	}

	body := []byte(`{"model":"claude-zen","messages":[{"role":"user","content":"hi"}]}`)
	c := visit(http.MethodPost, "/v1/chat/completions")
	c.Fiber().Request().SetBody(body)
	if out := c.pipeToFamily(zenFam, "chat/completions", "openai", "claude-zen", body, false, 0, "acme", nil, false, nil, time.Now()); out != nil {
		t.Fatalf("refused: %+v", out)
	}
	if asked, _ := z.heard(); len(asked) != 1 || asked[0] != "zen6" {
		t.Errorf("the family was sent %v, want zen6", asked)
	}
	var answer struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal([]byte(drain(t, c)), &answer); err != nil || answer.Model != "claude-zen" {
		t.Errorf("the answer names %q (%v), want claude-zen", answer.Model, err)
	}
}

// A family relay settles exactly what was served: an embedding worth a thousandth of
// a cent debits a thousandth of a cent and takes no whole cent off the balance, a
// rerank call priced at a tenth of a cent debits a tenth of a cent, and a rung the
// family says bills nothing debits nothing.
func TestAFamilyRelaySettlesSubCentChargesExactly(t *testing.T) {
	zenServiceAt(t, true)
	got := ledger(t)
	alice := &iam.User{Owner: "acme", Name: "alice"}
	subject := alice.PayerSubject("acme")
	object.GlobalBalanceLedger.SetBalance(subject, 100)

	// 1,000 tokens at $0.01 per million.
	body := []byte(`{"model":"zen-embedding","input":"hello"}`)
	c := visit(http.MethodPost, "/v1/embeddings")
	c.Fiber().Request().SetBody(body)
	hold, ok := reserveBudget(subject, 1)
	if !ok {
		t.Fatal("reserve refused")
	}
	if out := c.pipeToFamily(zenFam, "embeddings", "openai", "zen-embedding", body, false, 0, "acme", alice, true, hold, time.Now()); out != nil {
		t.Fatalf("refused: %+v", out)
	}
	if ev := got(); len(ev) != 1 || ev[0].USD != "0.00001" || ev[0].CostUSD != "0.000002" {
		t.Fatalf("an embedding of 1,000 tokens debited %+v, want $0.00001 at the family's COGS of $0.000002", ev)
	}
	if balance, reserved, _, _ := object.GlobalBalanceLedger.Snapshot(subject); balance != 100 || reserved != 0 {
		t.Errorf("the hold settled %d cents off the balance, %d still reserved; want none of a whole cent", 100-balance, reserved)
	}

	// One rerank call at $0.001.
	rr := []byte(`{"model":"zen-rerank","query":"q","documents":["a"]}`)
	c = visit(http.MethodPost, "/v1/rerank")
	c.Fiber().Request().SetBody(rr)
	c.serveZenMedia("rerank", "zen-rerank", rr, 1, "acme", alice, true, time.Now())
	if answered(c) != http.StatusOK {
		t.Fatalf("rerank answered %d %s", answered(c), sent(c))
	}
	if ev := got(); len(ev) != 2 || ev[1].USD != "0.001" {
		t.Fatalf("a rerank call debited %+v, want $0.001", ev)
	}

	// A priced SKU the family answered from a rung that bills nothing.
	w := whence{ledger: "acme", ip: c.Fiber().IP(), ctx: c.Context(), asked: zenFam}
	h := http.Header{}
	h.Set(freeHeader, "true")
	if nano := recordFamilyUsage(w, zenFam, "zen5-pro", "", nil, &mark{}, alice, true, false, "r", tokens{fresh: 1000, completion: 1000, reported: true}, servingOf(h), time.Now(), nil, "success", ""); nano != 0 {
		t.Errorf("a free rung billed %d nano", nano)
	}
	if ev := got(); len(ev) != 3 || ev[2].USD != "0" {
		t.Errorf("a free rung debited %+v, want $0", ev)
	}
}

// What a request cost upstream is the family's own statement — a trailer on a
// stream, a header on a whole answer — and it is the COGS the debit and the row
// carry. Each arm the family asked and could not use is a failover row beside the
// answer, under the same request, naming the arm, its vendor and why, billed nothing.
func TestTheFamilysCogsAndFailedArmsReachTheBooks(t *testing.T) {
	zenServiceAt(t, true)
	got := ledger(t)
	alice := &iam.User{Owner: "acme", Name: "alice"}

	body := []byte(`{"model":"zen5-pro","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	c := visit(http.MethodPost, "/v1/chat/completions")
	c.Fiber().Request().SetBody(body)
	if out := c.pipeToFamily(zenFam, "chat/completions", "openai", "zen5-pro", body, true, 0, "acme", alice, true, nil, time.Now()); out != nil {
		t.Fatalf("refused: %+v", out)
	}
	_ = drain(t, c)
	ev := got()
	// 100k prompt at $3/M and 10k completion at $15/M.
	if len(ev) != 1 || ev[0].USD != "0.45" || ev[0].CostUSD != "0.000123" {
		t.Fatalf("the stream debited %+v, want $0.45 at a COGS of $0.000123", ev)
	}

	whole := []byte(`{"model":"zen5-pro","messages":[{"role":"user","content":"hi"}]}`)
	c = visit(http.MethodPost, "/v1/chat/completions")
	c.Fiber().Request().SetBody(whole)
	if out := c.pipeToFamily(zenFam, "chat/completions", "openai", "zen5-pro", whole, false, 0, "acme", alice, true, nil, time.Now()); out != nil {
		t.Fatalf("refused: %+v", out)
	}
	_ = drain(t, c)
	if ev = got(); len(ev) != 2 || ev[1].CostUSD != "0.000123" {
		t.Fatalf("the whole answer debited %+v, want a COGS of $0.000123", ev)
	}

	h := http.Header{}
	h.Add(failoverHeader, "vendor/a (free): upstream status 429")
	h.Add(failoverHeader, "vendor/b (free): empty answer; retried once")
	w := whence{ledger: "acme", ip: "10.0.0.1", ctx: context.Background()}
	rows := failoverRows(w, zenFam, "zen5-pro", servingOf(h).failover, alice, true, true, "req-1")
	if len(rows) != 2 {
		t.Fatalf("%d failover rows, want one an arm", len(rows))
	}
	for i, want := range []struct{ served, vendor, msg string }{
		{"vendor/a", "free", "vendor/a: upstream status 429"},
		{"vendor/b", "free", "vendor/b: empty answer; retried once"},
	} {
		r := rows[i]
		if r.Status != "failover" || r.Served != want.served || r.Vendor != want.vendor || r.ErrorMsg != want.msg ||
			r.RequestID != "req-1" || r.Model != "zen5-pro" || r.Provider != "zen" || r.User != "acme/alice" {
			t.Errorf("row %d = %+v, want %+v", i, *r, want)
		}
		if n := usageMargin(r).BilledNano; n != 0 {
			t.Errorf("row %d bills %d nano", i, n)
		}
	}
	if rows := failoverRows(w, zenFam, "zen5-pro", "", alice, false, false, "req-2"); len(rows) != 0 {
		t.Errorf("no chain filed %d rows", len(rows))
	}
}

// A failover chain reads into its arms whether the family sends one field an arm or
// joins them in one, and a reason that carries "; " of its own stays whole.
func TestAFailoverChainReadsIntoItsArms(t *testing.T) {
	for chain, want := range map[string][]miss{
		"a/x (free): 429":                          {{"a/x", "free", "429"}},
		"a/x (free): 429; b/y (router): empty":     {{"a/x", "free", "429"}, {"b/y", "router", "empty"}},
		"a/x (free): read: eof; reset; b/y (r): 5": {{"a/x", "free", "read: eof; reset"}, {"b/y", "r", "5"}},
		"upstream status 502":                      {{"", "", "upstream status 502"}},
		"":                                         nil,
	} {
		got := arms(chain)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("arms(%q) = %+v, want %+v", chain, got, want)
		}
	}
}

// The family is told the org a call is for. A person's credential names its own
// org; a machine credential acting for a person — a sibling's call over the plane,
// which reaches the model API as the deployment and names the person in X-User-Id —
// names that person's org, never the deployment's.
func TestTheFamilyIsToldTheOrgACallIsFor(t *testing.T) {
	z := zenServiceAt(t, true)
	deployment := &iam.User{Owner: "hanzo", Name: "cloud", Type: "application"}
	cases := []struct {
		name string
		user *iam.User
		said string // X-User-Id
		want string
	}{
		{"a person", &iam.User{Owner: "acme", Name: "alice"}, "", "acme"},
		{"a machine for a person", deployment, "acme/alice", "acme"},
		{"a machine naming itself", deployment, "hanzo/cloud", "hanzo"},
		{"a machine naming nobody", deployment, "", "hanzo"},
	}
	for _, tc := range cases {
		body := []byte(`{"model":"zen-embedding","input":"hello"}`)
		c := visit(http.MethodPost, "/v1/embeddings")
		c.SetContext(object.WithGenAIAttribution(c.Context(), object.GenAIAttribution{User: tc.said}))
		c.Fiber().Request().SetBody(body)
		if out := c.pipeToFamily(zenFam, "embeddings", "openai", "zen-embedding", body, false, 0, tc.user.Owner, tc.user, true, nil, time.Now()); out != nil {
			t.Fatalf("%s: refused %+v", tc.name, out)
		}
		rr := []byte(`{"model":"zen-rerank","query":"q","documents":["a"]}`)
		c = visit(http.MethodPost, "/v1/rerank")
		c.SetContext(object.WithGenAIAttribution(c.Context(), object.GenAIAttribution{User: tc.said}))
		c.serveZenMedia("rerank", "zen-rerank", rr, 1, tc.user.Owner, tc.user, true, time.Now())
		_, orgs := z.heard()
		if got := orgs[len(orgs)-2:]; got[0] != tc.want || got[1] != tc.want {
			t.Errorf("%s: the family was told %v, want %s for embeddings and rerank", tc.name, got, tc.want)
		}
	}
}
