// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"context"
	"encoding/json"
	"net/http"
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
}

func (f *fakeZen) host() *RoutingHost {
	return &RoutingHost{
		Snapshot: func(context.Context) ([]byte, error) { f.mu.Lock(); defer f.mu.Unlock(); return []byte(f.cat), nil },
		Apply: func(_ context.Context, c []byte) error {
			if strings.Contains(string(c), "bad") {
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
	msg, err := zapRoutingHandler(context.Background(), method, path, "family=zen", auth, b)
	if err != nil {
		t.Fatal(err)
	}
	st, raw := int(vmiscStatus(msg)), vmiscBody(msg)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return st, out
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
	super := authtest.Bearer(t, iam.User{Owner: "admin", Name: "a"})

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
