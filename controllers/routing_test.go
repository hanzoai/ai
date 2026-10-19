// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/ai/internal/authtest"
	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
)

// fakeZen is a family catalog in memory that refuses a catalog carrying "bad".
type fakeZen struct {
	mu  sync.Mutex
	cat string
	// refuse is text the family will no longer take in a catalog.
	refuse string
}

func (f *fakeZen) host() *RoutingHost {
	return &RoutingHost{
		Snapshot: func(context.Context) ([]byte, error) { f.mu.Lock(); defer f.mu.Unlock(); return []byte(f.cat), nil },
		Apply: func(_ context.Context, c []byte) error {
			if strings.Contains(string(c), "bad") || (f.refuse != "" && strings.Contains(string(c), f.refuse)) {
				return &apiError{status: 400, msg: "zen: model \"x\" routes to unknown provider \"bad\""}
			}
			f.mu.Lock()
			f.cat = string(c)
			f.mu.Unlock()
			return nil
		},
	}
}

func routingCall(t *testing.T, auth, method, path string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	msg, err := zapRoutingHandler(bridgedAs(auth), method, path, "family=zen", auth, b)
	if err != nil {
		t.Fatal(err)
	}
	st, raw := int(vmiscStatus(msg)), vmiscBody(msg)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return st, out
}

// vouchedFor holds the tokens whose requests the host vouched for: what the HTTP
// bridge carries to the handler (vouch) for a request the boundary judged a
// SuperAdmin's. A token not in it reaches the handler as a raw ZAP door would.
var vouchedFor sync.Map

// superBearer is the SuperAdmin as the bridge serves one: an admin-org person
// whose request the host vouched for. The token alone is not one.
func superBearer(t *testing.T) string {
	t.Helper()
	tok := authtest.Bearer(t, iam.User{Owner: "admin", Name: "a"})
	vouchedFor.Store(tok, true)
	t.Cleanup(func() { vouchedFor.Delete(tok) })
	return tok
}

// bridgedAs is the context the HTTP bridge hands a handler for auth's request.
func bridgedAs(auth string) context.Context {
	_, ok := vouchedFor.Load(auth)
	return vouch(context.Background(), ok)
}

// A SuperAdmin proposes an edit and sees its diff, applies it against the newest
// version, and rolls it back; each apply is a version with its actor and diff, an
// edit made from an old version is refused, and a catalog the family refuses is
// neither applied nor recorded. No one else reaches any of it.
func TestRoutingIsVersionedAndSuperAdminOnly(t *testing.T) {
	withStore(t)
	t.Setenv("ZEN_URL", "")
	f := &fakeZen{cat: `{"models":{"zen6":{"route":[{"upstream":"a:free"}]}}}`}
	Zen = f.host()
	t.Cleanup(func() { Zen = nil })

	member := authtest.Bearer(t, iam.User{Owner: "acme", Name: "ann", IsAdmin: true})
	if st, _ := routingCall(t, member, http.MethodGet, "/v1/ai/router/catalog", nil); st != http.StatusForbidden {
		t.Fatalf("an org admin read the routing catalogs: %d", st)
	}
	if st, _ := routingCall(t, "", http.MethodGet, "/v1/ai/router/catalog", nil); st != http.StatusUnauthorized {
		t.Fatalf("an anonymous caller got %d", st)
	}
	// An admin-org token the host did not vouch for — a raw ZAP door — is no
	// SuperAdmin: the handler reads the verdict, never the org the token names.
	if st, _ := routingCall(t, authtest.Bearer(t, iam.User{Owner: "admin", Name: "b"}), http.MethodGet, "/v1/ai/router/catalog", nil); st != http.StatusForbidden {
		t.Fatalf("an admin-org token with no host verdict read the routing catalogs: %d", st)
	}
	super := superBearer(t)

	edit := map[string]any{"family": "zen", "catalog": json.RawMessage(`{"models":{"zen6":{"route":[{"upstream":"b:free"}]}}}`), "note": "b leads"}
	st, out := routingCall(t, super, http.MethodPost, "/v1/ai/router/catalog/propose", edit)
	diff, _ := out["data"].(map[string]any)["diff"].(string)
	if st != 200 || !hasLine(diff, "- ", `"upstream": "a:free"`) || !hasLine(diff, "+ ", `"upstream": "b:free"`) {
		t.Fatalf("propose %d diff:\n%s", st, diff)
	}
	if strings.Contains(f.cat, "b:free") {
		t.Fatal("a proposal changed the catalog")
	}

	edit["base"] = 0
	st, out = routingCall(t, super, http.MethodPut, "/v1/ai/router/catalog", edit)
	v1, _ := out["data"].(map[string]any)
	if st != 200 || v1["actor"] != "admin/a" || !strings.Contains(f.cat, "b:free") {
		t.Fatalf("apply %d %v, catalog %s", st, out, f.cat)
	}
	id1 := int64(v1["id"].(float64))

	if st, _ = routingCall(t, super, http.MethodPut, "/v1/ai/router/catalog", edit); st != http.StatusConflict {
		t.Fatalf("an edit made from version 0 after version %d applied: %d", id1, st)
	}

	bad := map[string]any{"family": "zen", "base": id1, "catalog": json.RawMessage(`{"providers":{"bad":{}}}`)}
	if st, _ = routingCall(t, super, http.MethodPut, "/v1/ai/router/catalog", bad); st != http.StatusBadRequest {
		t.Fatalf("a refused catalog answered %d", st)
	}
	if vs, _ := object.RoutingVersions("zen", 10); len(vs) != 1 {
		t.Fatalf("a refused catalog was recorded: %d versions", len(vs))
	}

	edit2 := map[string]any{"family": "zen", "base": id1, "catalog": json.RawMessage(`{"models":{"zen6":{"route":[{"upstream":"c:free"}]}}}`)}
	st, out = routingCall(t, super, http.MethodPut, "/v1/ai/router/catalog", edit2)
	id2 := int64(out["data"].(map[string]any)["id"].(float64))
	st, out = routingCall(t, super, http.MethodPost, "/v1/ai/router/catalog/rollback", map[string]any{"family": "zen", "version": id1, "base": id2})
	if st != 200 || !strings.Contains(f.cat, "b:free") || !strings.Contains(out["data"].(map[string]any)["note"].(string), "rollback") {
		t.Fatalf("rollback %d %v, catalog %s", st, out, f.cat)
	}

	// A family that restarted onto its file serves the newest version again.
	f.cat = `{"models":{"zen6":{"route":[{"upstream":"a:free"}]}}}`
	converge(context.Background(), "zen")
	if !strings.Contains(f.cat, "b:free") {
		t.Fatalf("convergence left %s", f.cat)
	}
}

func TestLineDiffShowsOnlyChangesAndContext(t *testing.T) {
	d := lineDiff("a\nb\nc\nd\ne\nf\ng", "a\nb\nc\nX\ne\nf\ng")
	if d != "  b\n  c\n- d\n+ X\n  e\n  f\n" {
		t.Fatalf("diff:\n%q", d)
	}
}

// hasLine reports whether diff has a line starting with op that contains text.
func hasLine(diff, op, text string) bool {
	for _, l := range strings.Split(diff, "\n") {
		if strings.HasPrefix(l, op) && strings.Contains(l, text) {
			return true
		}
	}
	return false
}

// engineFake is the engine's admin surface over servedRoute (hanzo-inc/engine
// root.rs): GET and PUT /v1/admin/catalog, GET /v1/admin/keys and /v1/admin/free,
// behind ZEN_ADMIN_TOKEN, and nothing at the Go zen's /v1/admin/zen/*. A catalog
// carrying "bad" is refused, as the engine refuses one that breaks its law.
type engineFake struct {
	mu    sync.Mutex
	cat   string
	paths []string
}

func (e *engineFake) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+zenServiceToken {
		http.Error(w, `{"error":{"message":"forbidden"}}`, http.StatusForbidden)
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.paths = append(e.paths, r.Method+" "+r.URL.Path)
	switch r.Method + " " + r.URL.Path {
	case "GET /v1/admin/catalog":
		_, _ = io.WriteString(w, e.cat)
	case "PUT /v1/admin/catalog":
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), "bad") {
			http.Error(w, `{"error":{"message":"enso: unknown provider bad"}}`, http.StatusBadRequest)
			return
		}
		e.cat = string(b)
		_, _ = io.WriteString(w, e.cat)
	case "GET /v1/admin/keys":
		_, _ = io.WriteString(w, `{"keys":[{"name":"OPENROUTER_API_KEY","remaining_usd":3}]}`)
	case "GET /v1/admin/free":
		_, _ = io.WriteString(w, `{"paid":true,"lanes":{}}`)
	default:
		http.Error(w, `{"error":{"message":"no such address"}}`, http.StatusNotFound)
	}
}

func engineAt(t *testing.T) *engineFake {
	t.Helper()
	e := &engineFake{cat: servedRoute}
	s := httptest.NewServer(http.HandlerFunc(e.serve))
	t.Cleanup(s.Close)
	t.Setenv("ENSO_URL", s.URL)
	t.Setenv("ZEN_ADMIN_TOKEN", zenServiceToken)
	return e
}

func ensoCall(t *testing.T, auth, method, path string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	msg, err := zapRoutingHandler(bridgedAs(auth), method, path, "family=enso", auth, b)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.Unmarshal(vmiscBody(msg), &out)
	return int(vmiscStatus(msg)), out
}

// catalogWith is servedRoute with sku's route replaced.
func catalogWith(t *testing.T, sku string, route ...string) json.RawMessage {
	t.Helper()
	var c map[string]any
	if err := json.Unmarshal([]byte(servedRoute), &c); err != nil {
		t.Fatal(err)
	}
	var rungs []any
	for _, r := range route {
		var rung any
		if err := json.Unmarshal([]byte(r), &rung); err != nil {
			t.Fatal(err)
		}
		rungs = append(rungs, rung)
	}
	c["models"].(map[string]any)[sku].(map[string]any)["route"] = rungs
	out, _ := json.Marshal(c)
	return out
}

const (
	sonnet = `{"provider":"openrouter","upstream":"anthropic/claude-sonnet-5.5","ctx":1000000,"cost":{"in":"2.20","out":"11.00","cache_read":"0.11","cache_write":"2.75"}}`
	opus   = `{"provider":"openrouter","upstream":"anthropic/claude-opus-5.5","ctx":1000000,"cost":{"in":"4.40","out":"22.00","cache_read":"0.22","cache_write":"5.50"}}`
)

// Enso is the engine's: its catalog is read and written on the engine's own admin
// routes, never the Go zen's /v1/admin/zen/*, and each edit is a version.
func TestEnsoIsEditedOnTheEnginesAdminRoutes(t *testing.T) {
	withStore(t)
	e := engineAt(t)
	super := superBearer(t)
	st, out := ensoCall(t, super, http.MethodGet, "/v1/ai/router/catalog", nil)
	views, _ := out["data"].([]any)
	if st != 200 || len(views) != 1 {
		t.Fatalf("read %d %v", st, out)
	}
	v := views[0].(map[string]any)
	if v["error"] != nil || v["catalog"] == nil {
		t.Fatalf("enso read: %v", v)
	}
	if keys, _ := v["stats"].(map[string]any)["keys"].(map[string]any); keys == nil {
		t.Fatalf("enso stats carry no keys: %v", v["stats"])
	}
	edit := map[string]any{"family": "enso", "base": 0, "note": "sonnet leads pro",
		"catalog": catalogWith(t, "enso-pro", sonnet)}
	if st, out = ensoCall(t, super, http.MethodPut, "/v1/ai/router/catalog", edit); st != 200 {
		t.Fatalf("apply %d %v", st, out)
	}
	if got := leads(t, e.cat, "enso-pro"); len(got) != 1 || got[0] != "anthropic/claude-sonnet-5.5" {
		t.Fatalf("enso-pro after apply: %v", got)
	}
	for _, p := range e.paths {
		if strings.Contains(p, "/v1/admin/zen/") {
			t.Fatalf("asked the engine at the Go zen's address: %v", e.paths)
		}
	}
	if vs, _ := object.RoutingVersions("enso", 10); len(vs) != 1 || vs[0].Actor != "admin/a" {
		t.Fatalf("versions %v", vs)
	}
}

// A catalog whose route the price guard would refuse is not applied: an arm that
// costs more than its model bills, OpenRouter's fee on it, would leave the model
// answering 503. A proposal says so before anyone confirms it, and the apply refuses
// it with the same reason.
func TestACatalogTheGuardWouldRefuseIsNotApplied(t *testing.T) {
	withStore(t)
	orFees(t)
	e := engineAt(t)
	super := superBearer(t)
	over := catalogWith(t, "enso-pro", opus, sonnet)

	st, out := ensoCall(t, super, http.MethodPost, "/v1/ai/router/catalog/propose", map[string]any{"family": "enso", "catalog": over})
	p, _ := out["data"].(map[string]any)
	if st != 200 || !strings.Contains(p["refused"].(string), "anthropic/claude-opus-5.5") || !strings.Contains(p["refused"].(string), "enso-pro") {
		t.Fatalf("propose %d %v", st, out)
	}
	st, out = ensoCall(t, super, http.MethodPut, "/v1/ai/router/catalog", map[string]any{"family": "enso", "base": 0, "catalog": over})
	if st != http.StatusBadRequest || !strings.Contains(out["msg"].(string), "claude-opus-5.5") {
		t.Fatalf("apply over the price: %d %v", st, out)
	}
	if e.cat != servedRoute {
		t.Fatal("a catalog the guard refuses was applied")
	}
	if vs, _ := object.RoutingVersions("enso", 10); len(vs) != 0 {
		t.Fatalf("a refused catalog was recorded: %d", len(vs))
	}
	// The same arm behind enso-ultra, which bills enough for it, is applied.
	fits := catalogWith(t, "enso-ultra", opus, sonnet)
	if st, out = ensoCall(t, super, http.MethodPut, "/v1/ai/router/catalog", map[string]any{"family": "enso", "base": 0, "catalog": fits}); st != 200 {
		t.Fatalf("apply under the price: %d %v", st, out)
	}
}

// Convergence serves the newest version again on the deployment it was made over,
// and never over another: a family that restarted onto a changed catalog (a new
// release of its file) keeps serving that, and the read says so.
func TestConvergenceLeavesANewDeploymentStanding(t *testing.T) {
	withStore(t)
	t.Setenv("ZEN_URL", "")
	f := &fakeZen{cat: `{"models":{"zen6":{"route":[{"upstream":"a:free"}]}}}`}
	Zen = f.host()
	t.Cleanup(func() { Zen = nil })
	ctx := context.Background()
	v1, _, err := RoutingApply(ctx, "zen", json.RawMessage(`{"models":{"zen6":{"route":[{"upstream":"b:free"}]}}}`), 0, "admin/a", "b leads")
	if err != nil {
		t.Fatal(err)
	}
	v2, _, err := RoutingApply(ctx, "zen", json.RawMessage(`{"models":{"zen6":{"route":[{"upstream":"c:free"}]}}}`), v1.Id, "admin/a", "c leads")
	if err != nil {
		t.Fatal(err)
	}
	if v2.Boot != v1.Boot || v1.Boot == "" {
		t.Fatalf("a version made from the newest keeps its deployment: %q then %q", v1.Boot, v2.Boot)
	}

	// Restarted onto the same file: the newest version again.
	f.cat = `{"models":{"zen6":{"route":[{"upstream":"a:free"}]}}}`
	converge(ctx, "zen")
	if !strings.Contains(f.cat, "c:free") {
		t.Fatalf("restarted onto its file, the family serves %s", f.cat)
	}

	// Restarted onto a new release of its file: that stands.
	f.cat = `{"models":{"zen6":{"route":[{"upstream":"d:free"}]},"zen7":{"route":[{"upstream":"e:free"}]}}}`
	converge(ctx, "zen")
	if !strings.Contains(f.cat, "d:free") || !strings.Contains(f.cat, "zen7") {
		t.Fatalf("a new deployment was overwritten: %s", f.cat)
	}
	view := RoutingRead(ctx, "zen")
	if !view.Drift || !view.Redeployed {
		t.Fatalf("drift %v redeployed %v", view.Drift, view.Redeployed)
	}

	// An edit made from what serves then starts over on the new deployment.
	v3, _, err := RoutingApply(ctx, "zen", json.RawMessage(`{"models":{"zen6":{"route":[{"upstream":"f:free"}]},"zen7":{"route":[{"upstream":"e:free"}]}}}`), v2.Id, "admin/a", "f leads")
	if err != nil {
		t.Fatal(err)
	}
	if v3.Boot == v2.Boot {
		t.Fatal("an edit over a new deployment kept the old one's")
	}
	if view = RoutingRead(ctx, "zen"); view.Drift || view.Redeployed {
		t.Fatalf("after the edit: drift %v redeployed %v", view.Drift, view.Redeployed)
	}
}

// leads is the upstreams of sku's route in catalog, in order.
func leads(t *testing.T, catalog, sku string) []string {
	t.Helper()
	var c struct {
		Models map[string]struct {
			Route []struct {
				Upstream string `json:"upstream"`
			} `json:"route"`
		} `json:"models"`
	}
	if err := json.Unmarshal([]byte(catalog), &c); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range c.Models[sku].Route {
		out = append(out, r.Upstream)
	}
	return out
}

// An edit made while a family has restarted onto the deployment its newest version
// was made over, before convergence serves that version again, is refused: made
// from what serves, it would drop the newest version's own edits. Once convergence
// has run, the same edit applies.
func TestAnEditOverARestartWaitsForTheNewestVersion(t *testing.T) {
	withStore(t)
	t.Setenv("ZEN_URL", "")
	file := `{"models":{"zen6":{"route":[{"upstream":"a:free"}]}}}`
	f := &fakeZen{cat: file}
	Zen = f.host()
	t.Cleanup(func() { Zen = nil })
	ctx := context.Background()
	v1, _, err := RoutingApply(ctx, "zen", json.RawMessage(`{"paid":false,"models":{"zen6":{"route":[{"upstream":"b:free"}]}}}`), 0, "admin/a", "paid lane off")
	if err != nil {
		t.Fatal(err)
	}
	f.cat = file // restarted onto its file
	edit := json.RawMessage(`{"models":{"zen6":{"route":[{"upstream":"c:free"}]}}}`)
	if _, status, err := RoutingApply(ctx, "zen", edit, v1.Id, "admin/a", "c leads"); status != http.StatusConflict || err == nil || !strings.Contains(err.Error(), "restarted") {
		t.Fatalf("an edit over a restart: %d %v", status, err)
	}
	converge(ctx, "zen")
	if !strings.Contains(f.cat, `"b:free"`) {
		t.Fatalf("convergence left %s", f.cat)
	}
	if _, _, err := RoutingApply(ctx, "zen", edit, v1.Id, "admin/a", "c leads"); err != nil {
		t.Fatalf("the edit once the newest serves again: %v", err)
	}
}

// What a family's admin view derives (a key that resolves, a rung's retail and
// profit, the margin in effect) is no part of its deployment: a restart that turns
// a key on or off, or an engine that derives another figure, still serves the
// newest version again.
func TestADerivedFieldIsNoNewDeployment(t *testing.T) {
	withStore(t)
	t.Setenv("ZEN_URL", "")
	file := `{"providers":{"or":{"keyPresent":true}},"models":{"zen6":{"effectiveMargin":"3","route":[{"upstream":"a:free","retail":{"in":"1"}}]}}}`
	f := &fakeZen{cat: file}
	Zen = f.host()
	t.Cleanup(func() { Zen = nil })
	ctx := context.Background()
	if _, _, err := RoutingApply(ctx, "zen", json.RawMessage(`{"providers":{"or":{"keyPresent":true}},"models":{"zen6":{"effectiveMargin":"3","route":[{"upstream":"b:free","retail":{"in":"1"}}]}}}`), 0, "admin/a", "b leads"); err != nil {
		t.Fatal(err)
	}
	f.cat = `{"providers":{"or":{"keyPresent":false}},"models":{"zen6":{"effectiveMargin":"2.5","route":[{"upstream":"a:free","retail":{"in":"0.9"},"profit":{"in":"0"}}]}}}`
	if v := RoutingRead(ctx, "zen"); !v.Drift || v.Redeployed {
		t.Fatalf("drift %v redeployed %v: a derived field read as a new deployment", v.Drift, v.Redeployed)
	}
	converge(ctx, "zen")
	if !strings.Contains(f.cat, "b:free") {
		t.Fatalf("convergence left %s", f.cat)
	}
}

// A version a family will no longer take (an engine that dropped a field the
// version still names) does not lock edits out: convergence serves it less what
// the family derives, and once that fails on a deployment, an edit made from what
// serves is let through.
func TestAFamilyThatWillNotTakeTheNewestStillTakesAnEdit(t *testing.T) {
	withStore(t)
	t.Setenv("ZEN_URL", "")
	file := `{"models":{"zen6":{"route":[{"upstream":"a:free","retail":{"in":"1"}}]}}}`
	f := &fakeZen{cat: file}
	Zen = f.host()
	t.Cleanup(func() { Zen = nil })
	ctx := context.Background()
	v1, _, err := RoutingApply(ctx, "zen", json.RawMessage(`{"models":{"zen6":{"route":[{"upstream":"b:free","retail":{"in":"1"}}]}}}`), 0, "admin/a", "b leads")
	if err != nil {
		t.Fatal(err)
	}
	// Restarted; the stored version is served less its derived fields.
	f.cat = file
	converge(ctx, "zen")
	if !strings.Contains(f.cat, "b:free") || strings.Contains(f.cat, "retail") {
		t.Fatalf("convergence sent %s", f.cat)
	}
	// Restarted again, and the family now refuses what the version names.
	f.cat = file
	f.refuse = "b:free"
	if _, status, _ := RoutingApply(ctx, "zen", json.RawMessage(`{"models":{"zen6":{"route":[{"upstream":"c:free"}]}}}`), v1.Id, "admin/a", ""); status != http.StatusConflict {
		t.Fatalf("an edit before convergence tried: %d", status)
	}
	converge(ctx, "zen")
	if _, _, err := RoutingApply(ctx, "zen", json.RawMessage(`{"models":{"zen6":{"route":[{"upstream":"c:free"}]}}}`), v1.Id, "admin/a", "c leads"); err != nil {
		t.Fatalf("an edit once convergence failed: %v", err)
	}
}
