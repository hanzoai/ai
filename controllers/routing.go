// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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

package controllers

// routing.go — /v1/ai/router/catalog: the Zen and Enso routing catalogs as a SuperAdmin
// edits them at admin.hanzo.ai.
//
// A family's catalog is what decides which upstream models answer each of its SKUs,
// in what order, on which key pool, and whether the paid lane is open. Two
// families are edited here:
//
//	zen   served by the Go zen service at ZEN_URL, through its admin routes under
//	      /v1/admin/zen; or, where a host links zen into this process, by that zen
//	      (Zen), with each edit passed on to the service at ZEN_URL.
//	enso  served by the engine at ENSO_URL (hanzo-inc/engine), through its admin
//	      routes under /v1/admin.
//
// Both admin surfaces take the ZEN_ADMIN_TOKEN key.
//
// An edit is proposed (answered with its diff, and with the reason the family's price
// guard would refuse it, if it would), then applied against the version it was made
// from: a catalog the guard would refuse is not sent, the family takes the rest or
// refuses it with the reason, and a version row records the catalog, who applied it,
// when, the diff, and the deployment its run of versions was made over (Boot).
// Rolling back applies an older version's catalog as a new version. Every minute the
// newest version is compared with what each family serves and applied again where
// the family restarted onto the deployment the version was made over; a family that
// serves another (a new release of its file) is left standing, and its read says so.
//
// Everything here is SuperAdmin only (zapMiscSuperAdmin): the catalogs name the
// upstreams behind every SKU, which no other caller is shown.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/object"
	"github.com/luxfi/zap"
)

// RoutingHost is a family catalog served in this process: its snapshot and the one
// write path onto it, both in the family's admin JSON. The host sets Zen at mount.
type RoutingHost struct {
	Snapshot func(ctx context.Context) ([]byte, error)
	Apply    func(ctx context.Context, catalog []byte) error
	// Stats is the family's live standing (key balances, the free lane's model
	// health), JSON; nil when the host has none.
	Stats func(ctx context.Context) ([]byte, error)
}

// Zen is the zen catalog served in this process, nil where no host links zen in:
// the zen service at ZEN_URL is then the catalog, edited over its admin routes as
// enso's is.
var Zen *RoutingHost

// routingFamilies are the families this surface edits.
var routingFamilies = []string{"zen", "enso"}

// routingHost resolves a family to its catalog: zen in process where a host mounted
// it, else each family over HTTP at its service.
func routingHost(family string) (*RoutingHost, error) {
	switch family {
	case "zen":
		base := strings.TrimRight(zenFam.baseURL(), "/")
		if Zen == nil {
			if base == "" {
				return nil, fmt.Errorf("zen is not configured")
			}
			return remoteRouting(base, zenAdmin), nil
		}
		// The zen service at ZEN_URL serves the same family outside this process
		// and takes every edit after this one.
		if base == "" {
			return Zen, nil
		}
		standalone := remoteRouting(base, zenAdmin)
		return &RoutingHost{
			Snapshot: Zen.Snapshot,
			Stats:    Zen.Stats,
			Apply: func(ctx context.Context, catalog []byte) error {
				if err := Zen.Apply(ctx, catalog); err != nil {
					return err
				}
				if err := standalone.Apply(ctx, catalog); err != nil {
					log.Warn("routing: the zen service did not take the catalog: %v", err)
				}
				return nil
			},
		}, nil
	case "enso":
		base := strings.TrimRight(ensoFam.baseURL(), "/")
		if base == "" {
			return nil, fmt.Errorf("enso is not configured")
		}
		return remoteRouting(base, engineAdmin), nil
	}
	return nil, fmt.Errorf("unknown family %q (want zen or enso)", family)
}

// The admin routes each kind of family service answers under: the Go zen's, and the
// engine's (hanzo-inc/engine root.rs: /v1/admin/catalog, /keys, /free).
const (
	zenAdmin    = "/v1/admin/zen"
	engineAdmin = "/v1/admin"
)

// remoteRouting is a family served at base, through its admin routes under prefix
// and the ZEN_ADMIN_TOKEN key.
func remoteRouting(base, prefix string) *RoutingHost {
	call := func(ctx context.Context, method, path string, body []byte) ([]byte, error) {
		token := strings.TrimSpace(object.ResolveKey("ZEN_ADMIN_TOKEN"))
		if token == "" {
			return nil, fmt.Errorf("ZEN_ADMIN_TOKEN is not set")
		}
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := zenDiscoveryClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		out, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, upstreamErrorMessage(out))
		}
		return out, nil
	}
	return &RoutingHost{
		Snapshot: func(ctx context.Context) ([]byte, error) {
			return call(ctx, http.MethodGet, prefix+"/catalog", nil)
		},
		Apply: func(ctx context.Context, catalog []byte) error {
			_, err := call(ctx, http.MethodPut, prefix+"/catalog", catalog)
			return err
		},
		Stats: func(ctx context.Context) ([]byte, error) {
			keys, err := call(ctx, http.MethodGet, prefix+"/keys", nil)
			if err != nil {
				return nil, err
			}
			free, _ := call(ctx, http.MethodGet, prefix+"/free", nil)
			if len(free) == 0 {
				free = []byte("null")
			}
			return json.Marshal(map[string]json.RawMessage{"keys": keys, "free": free})
		},
	}
}

// derived are the fields a family's admin view computes rather than holds
// (hanzo-inc/engine admin.rs): whether a key resolves, the margin in effect, the
// tier floor and funding a model's providers imply, and each rung's retail and
// profit. They move with a key or an engine release, never with the deployment.
var derived = struct{ provider, model, rung []string }{
	provider: []string{"keyPresent"},
	model:    []string{"effectiveMargin", "minTier", "funding"},
	rung:     []string{"retail", "profit"},
}

// deployment is the digest of what a family serves less every derived field: the
// catalog its deployment and the edits made to it say, which a restart onto the
// same file reproduces whatever key resolved or engine reads it.
func deployment(catalog string) string {
	return object.CatalogSum(bare(catalog))
}

// bare is a catalog less every derived field: what an edit says and a family takes
// back whatever it derives, so an engine that drops or renames a derived field
// still takes a version stored before it did. A catalog that is not JSON is itself.
func bare(catalog string) string {
	var c map[string]any
	d := json.NewDecoder(strings.NewReader(catalog))
	d.UseNumber()
	if err := d.Decode(&c); err != nil {
		return catalog
	}
	drop := func(m any, keys []string) {
		if o, ok := m.(map[string]any); ok {
			for _, k := range keys {
				delete(o, k)
			}
		}
	}
	if ps, ok := c["providers"].(map[string]any); ok {
		for _, p := range ps {
			drop(p, derived.provider)
		}
	}
	if ms, ok := c["models"].(map[string]any); ok {
		for _, m := range ms {
			drop(m, derived.model)
			if o, ok := m.(map[string]any); ok {
				if rs, ok := o["route"].([]any); ok {
					for _, r := range rs {
						drop(r, derived.rung)
					}
				}
			}
		}
	}
	b, err := json.Marshal(c)
	if err != nil {
		return catalog
	}
	return string(b)
}

// canonical re-encodes a catalog with sorted keys, so two catalogs that say the
// same thing compare equal and diff line by line.
func canonicalJSON(raw []byte) (string, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", fmt.Errorf("catalog is not JSON: %w", err)
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// lineDiff is a unified-style diff of two texts, line by line: "-" lines only in a,
// "+" lines only in b, with up to two lines of context around each change.
func lineDiff(a, b string) string {
	x, y := strings.Split(a, "\n"), strings.Split(b, "\n")
	n, m := len(x), len(y)
	// Longest common subsequence table; catalogs are a few thousand lines.
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	type line struct {
		op   byte
		text string
	}
	var all []line
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && x[i] == y[j]:
			all = append(all, line{' ', x[i]})
			i, j = i+1, j+1
		case i < n && (j == m || lcs[i+1][j] >= lcs[i][j+1]):
			all = append(all, line{'-', x[i]})
			i++
		default:
			all = append(all, line{'+', y[j]})
			j++
		}
	}
	keep := make([]bool, len(all))
	for k, l := range all {
		if l.op != ' ' {
			for d := max(0, k-2); d <= min(len(all)-1, k+2); d++ {
				keep[d] = true
			}
		}
	}
	var out strings.Builder
	gap := false
	for k, l := range all {
		if !keep[k] {
			gap = true
			continue
		}
		if gap && out.Len() > 0 {
			out.WriteString("…\n")
		}
		gap = false
		out.WriteByte(l.op)
		out.WriteByte(' ')
		out.WriteString(l.text)
		out.WriteByte('\n')
	}
	return out.String()
}

// RoutingView is what GET /v1/ai/router/catalog answers for one family.
type RoutingView struct {
	Family   string                   `json:"family"`
	Catalog  json.RawMessage          `json:"catalog"`         // what the family serves now
	Stats    json.RawMessage          `json:"stats,omitempty"` // key balances and free-lane health
	Version  *object.RoutingVersion   `json:"version"`         // the newest applied version, nil before the first edit
	Versions []*object.RoutingVersion `json:"versions"`        // history, newest first (catalogs left out)
	Drift    bool                     `json:"drift"`           // the family serves something other than its newest version
	// Redeployed is drift on a deployment the newest version was not made over (a
	// new release of the family's file): convergence leaves it standing, so the
	// newest version serves again only when it is applied again.
	Redeployed bool   `json:"redeployed"`
	Error      string `json:"error,omitempty"`
}

// The typed HTTP bindings of /v1/ai/router/catalog: one handler per operation, so
// the published document can say what each takes and answers. Each dispatches to
// zapRoutingHandler, the one implementation, through the router-config bridge.

// RouterCatalogRead lists the Zen and Enso routing catalogs: what each serves, its
// accounts and model health, and its version history. SuperAdmin only.
func (c *ApiController) RouterCatalogRead() { c.RouterConfigBridge() }

// RouterCatalogApply applies an edited catalog to one family, made from its newest
// version, and records the new version. SuperAdmin only.
func (c *ApiController) RouterCatalogApply() { c.RouterConfigBridge() }

// RouterCatalogPropose answers the diff an edited catalog would apply to a family,
// and the version it would be made from. Nothing is applied. SuperAdmin only.
func (c *ApiController) RouterCatalogPropose() { c.RouterConfigBridge() }

// RouterCatalogRollback applies an earlier version of a family's catalog again, as
// a new version. SuperAdmin only.
func (c *ApiController) RouterCatalogRollback() { c.RouterConfigBridge() }

// RouterCatalogTest sends one short turn to a SKU through its family and answers
// which upstream wrote it and how long it took. SuperAdmin only.
func (c *ApiController) RouterCatalogTest() { c.RouterConfigBridge() }

// RoutingProposal is what a proposal answers.
type RoutingProposal struct {
	Family string `json:"family"`
	Base   int64  `json:"base"`
	Diff   string `json:"diff"`
	// Refused is why the family's price guard would refuse a model the catalog
	// serves (the arm over its price, and in which classes), "" when every priced
	// model fits. An apply refuses such a catalog with the same reason.
	Refused string `json:"refused,omitempty"`
}

// routingProbe is what a test turn answers.
type routingProbe struct {
	Status   int    `json:"status"`
	SKU      string `json:"sku"`
	Arm      string `json:"arm"`
	Failover string `json:"failover"`
	Ms       int64  `json:"ms"`
	Reply    string `json:"reply"`
}

type routingEdit struct {
	Family  string          `json:"family"`
	Catalog json.RawMessage `json:"catalog"`
	Base    int64           `json:"base"`    // the version the edit was made from; 0 before the first
	Note    string          `json:"note"`    // why, for the history
	Version int64           `json:"version"` // rollback: the version to apply again
	Model   string          `json:"model"`   // test: the SKU to send one turn to
}

func routingFamily(query string, body []byte) string {
	if q, err := url.ParseQuery(query); err == nil && q.Get("family") != "" {
		return strings.TrimSpace(q.Get("family"))
	}
	var e routingEdit
	_ = json.Unmarshal(body, &e)
	return strings.TrimSpace(e.Family)
}

// zapRoutingHandler serves /v1/ai/router/catalog and its /propose, /rollback and /test
// sub-paths.
func zapRoutingHandler(ctx context.Context, method, path, query, auth string, body []byte) (*zap.Message, error) {
	user := zapPrincipal(ctx, auth)
	if deny := zapMiscAuthz("routing", user); deny != nil {
		return deny, nil
	}
	actor := user.Owner + "/" + user.Name
	sub := strings.TrimPrefix(strings.TrimPrefix(path, "/v1/ai/router/catalog"), "/")
	method = strings.ToUpper(method)
	switch {
	case sub == "" && method == http.MethodGet:
		families := routingFamilies
		if f := routingFamily(query, nil); f != "" {
			families = []string{f}
		}
		out := make([]RoutingView, 0, len(families))
		for _, f := range families {
			out = append(out, RoutingRead(ctx, f))
		}
		return zapOk(out)
	case sub == "propose" && method == http.MethodPost:
		var e routingEdit
		if err := json.Unmarshal(body, &e); err != nil {
			return zapError(http.StatusBadRequest, "invalid request: "+err.Error())
		}
		p, err := RoutingPropose(ctx, e.Family, e.Catalog)
		if err != nil {
			return zapError(http.StatusBadRequest, err.Error())
		}
		return zapOk(p)
	case sub == "" && method == http.MethodPut:
		var e routingEdit
		if err := json.Unmarshal(body, &e); err != nil {
			return zapError(http.StatusBadRequest, "invalid request: "+err.Error())
		}
		v, status, err := RoutingApply(ctx, e.Family, e.Catalog, e.Base, actor, e.Note)
		if err != nil {
			return zapError(status, err.Error())
		}
		return zapOk(v)
	case sub == "rollback" && method == http.MethodPost:
		var e routingEdit
		if err := json.Unmarshal(body, &e); err != nil {
			return zapError(http.StatusBadRequest, "invalid request: "+err.Error())
		}
		old, err := object.GetRoutingVersion(e.Version)
		if err != nil || old == nil || old.Family != e.Family {
			return zapError(http.StatusNotFound, fmt.Sprintf("no %s version %d", e.Family, e.Version))
		}
		note := strings.TrimSpace(e.Note)
		if note == "" {
			note = "rollback to version " + strconv.FormatInt(old.Id, 10)
		}
		v, status, err := RoutingApply(ctx, e.Family, json.RawMessage(old.Catalog), e.Base, actor, note)
		if err != nil {
			return zapError(status, err.Error())
		}
		return zapOk(v)
	case sub == "test" && method == http.MethodPost:
		var e routingEdit
		if err := json.Unmarshal(body, &e); err != nil {
			return zapError(http.StatusBadRequest, "invalid request: "+err.Error())
		}
		out, err := routingTest(ctx, e.Family, e.Model)
		if err != nil {
			return zapError(http.StatusBadGateway, err.Error())
		}
		return zapOk(out)
	}
	return zapError(http.StatusMethodNotAllowed, "method not allowed: "+method+" "+path)
}

// RoutingRead is one family's routing as GET /v1/ai/router/catalog answers it: what
// it serves, its standing, its versions, and whether it serves its newest version.
// A family that cannot be read says why in Error.
func RoutingRead(ctx context.Context, family string) RoutingView {
	v := RoutingView{Family: family, Versions: []*object.RoutingVersion{}}
	h, err := routingHost(family)
	if err != nil {
		v.Error = err.Error()
		return v
	}
	if snap, err := h.Snapshot(ctx); err != nil {
		v.Error = err.Error()
	} else {
		v.Catalog = snap
	}
	if h.Stats != nil {
		if s, err := h.Stats(ctx); err == nil {
			v.Stats = s
		}
	}
	if vs, err := object.RoutingVersions(family, 50); err == nil {
		for i, row := range vs {
			if i == 0 {
				full := *row
				v.Version = &full
			}
			short := *row
			short.Catalog = ""
			v.Versions = append(v.Versions, &short)
		}
	}
	if v.Version != nil && len(v.Catalog) > 0 {
		if live, err := canonicalJSON(v.Catalog); err == nil {
			v.Drift = object.CatalogSum(live) != v.Version.Sum
			v.Redeployed = v.Drift && deployment(live) != v.Version.Boot
		}
	}
	return v
}

// RoutingPropose answers the diff catalog would apply to family against what it
// serves now, the version it would be made from, and why the family's price guard
// would refuse it, if it would. Nothing is applied.
func RoutingPropose(ctx context.Context, family string, catalog json.RawMessage) (RoutingProposal, error) {
	p := RoutingProposal{Family: family}
	h, err := routingHost(family)
	if err != nil {
		return p, err
	}
	next, err := canonicalJSON(catalog)
	if err != nil {
		return p, err
	}
	snap, err := h.Snapshot(ctx)
	if err != nil {
		return p, err
	}
	live, err := canonicalJSON(snap)
	if err != nil {
		return p, err
	}
	if latest, _ := object.LatestRoutingVersion(family); latest != nil {
		p.Base = latest.Id
	}
	if p.Refused, err = guardRefuses(family, []byte(next)); err != nil {
		return p, err
	}
	p.Diff = lineDiff(live, next)
	return p, nil
}

// guardRefuses is why family's price guard would refuse a model catalog serves: the
// first priced model whose route costs more than it bills, the arm and the classes
// (misfit). "" when every priced model the catalog serves fits, and for a family
// priced any other way. It names upstreams, so it is for a SuperAdmin.
func guardRefuses(family string, catalog []byte) (string, error) {
	f := familyNamed(family)
	if f == nil || f.prices == nil {
		return "", nil
	}
	rs, err := rungsOf(catalog)
	if err != nil {
		return "", fmt.Errorf("catalog: %w", err)
	}
	for _, id := range slices.Sorted(maps.Keys(f.prices)) {
		route, served := rs[id]
		if !served {
			continue
		}
		c := f.prices[id].price()
		m := zenModel{ID: id, Base: zenTier{In: c.in, Out: c.out, CacheRead: c.read, CacheWrite: c.write}}
		if why := misfit(m, route); why != "" {
			return why, nil
		}
	}
	return "", nil
}

// routingMu serializes applies, so the base check and the write are one step.
var routingMu sync.Mutex

// RoutingApply applies catalog to family and records it, refusing an edit made from
// any version but the newest and a catalog the family's price guard would refuse.
func RoutingApply(ctx context.Context, family string, catalog json.RawMessage, base int64, actor, note string) (*object.RoutingVersion, int, error) {
	routingMu.Lock()
	defer routingMu.Unlock()
	h, err := routingHost(family)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	next, err := canonicalJSON(catalog)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	var latestID int64
	latest, _ := object.LatestRoutingVersion(family)
	if latest != nil {
		latestID = latest.Id
	}
	if base != latestID {
		return nil, http.StatusConflict, fmt.Errorf("%s changed since this edit was made (version %d is newest, the edit was made from %d); reload and edit again", family, latestID, base)
	}
	if why, err := guardRefuses(family, []byte(next)); err != nil {
		return nil, http.StatusBadRequest, err
	} else if why != "" {
		return nil, http.StatusBadRequest, fmt.Errorf("%s would not be served: %s", family, why)
	}
	snap, err := h.Snapshot(ctx)
	if err != nil {
		return nil, http.StatusBadGateway, err
	}
	live, _ := canonicalJSON(snap)
	// The run this version joins: the newest's, when the family serves the newest;
	// else what the family serves now, a deployment of its own.
	boot := deployment(live)
	if latest != nil && latest.Boot != "" {
		switch {
		case object.CatalogSum(live) == latest.Sum:
			boot = latest.Boot
		case boot == latest.Boot && unconverged[family] != boot:
			// Restarted onto the deployment the newest version was made over, and
			// not yet served it again: an edit made from what serves now would drop
			// the newest version's own edits. Once convergence has failed to serve
			// it on this deployment, an edit is the way forward and is let through.
			return nil, http.StatusConflict, fmt.Errorf("%s restarted and serves its deployment, not version %d yet; it is applied again within a minute: reload and edit again", family, latestID)
		}
	}
	if err := h.Apply(ctx, []byte(next)); err != nil {
		// The family refused it: the catalog breaks its law (a price, a provider, a
		// fallback). Nothing was applied and nothing is recorded.
		return nil, http.StatusBadRequest, fmt.Errorf("%s refused the catalog: %v", family, err)
	}
	// What the family serves after the edit is the version: its own reading of the
	// catalog, with every derived field it fills in.
	applied := next
	if after, err := h.Snapshot(ctx); err == nil {
		if c, err := canonicalJSON(after); err == nil {
			applied = c
		}
	}
	v, err := object.AddRoutingVersion(&object.RoutingVersion{
		Family: family, Catalog: applied, Base: base, Actor: actor, Note: note, Diff: lineDiff(live, applied), Boot: boot,
	})
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("%s applied the catalog, and its version was not recorded: %v", family, err)
	}
	log.Info("routing: %s applied version %d by %s: %s", family, v.Id, actor, note)
	return v, http.StatusOK, nil
}

// routingTest sends one short turn to model through the family and answers the
// model that wrote it and how long it took.
func routingTest(ctx context.Context, family, model string) (*routingProbe, error) {
	var base string
	switch family {
	case "enso":
		base = strings.TrimRight(ensoFam.baseURL(), "/")
	case "zen":
		base = strings.TrimRight(zenFam.baseURL(), "/")
	}
	if base == "" || strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("family %q or model %q cannot be tested here", family, model)
	}
	body, _ := json.Marshal(map[string]any{"model": model, "max_tokens": 40,
		"messages": []map[string]string{{"role": "user", "content": "Reply with the single word ready."}}})
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", "admin")
	req.Header.Set("X-Hanzo-Fronted-By", "ai")
	start := time.Now()
	resp, err := zenPipeClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var r struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	_ = json.Unmarshal(out, &r)
	text := ""
	if len(r.Choices) > 0 {
		text = strings.TrimSpace(r.Choices[0].Message.Content)
	}
	return &routingProbe{
		Status: resp.StatusCode, SKU: resp.Header.Get(servedHeader), Arm: resp.Header.Get(armHeader),
		Failover: resp.Header.Get(failoverHeader), Ms: time.Since(start).Milliseconds(), Reply: text,
	}, nil
}

// routingEvery is how often each family's newest version is checked against what
// it serves.
const routingEvery = time.Minute

// StartRoutingConvergence applies each family's newest version wherever the family
// restarted onto the deployment that version was made over.
func StartRoutingConvergence() {
	routingOnce.Do(func() {
		go func() {
			for {
				for _, f := range routingFamilies {
					converge(context.Background(), f)
				}
				time.Sleep(routingEvery)
			}
		}()
	})
}

var routingOnce sync.Once

// unconverged is, per family, the deployment convergence last failed to serve its
// newest version on; held under routingMu.
var unconverged = map[string]string{}

// stood is the catalog each family was last left serving over its newest version,
// so the warning is written once a deployment, not once a minute.
var (
	stoodMu sync.Mutex
	stood   = map[string]string{}
)

func leftStanding(family string, version int64, sum string) {
	stoodMu.Lock()
	defer stoodMu.Unlock()
	if stood[family] == sum {
		return
	}
	stood[family] = sum
	log.Warn("routing: %s serves a deployment version %d was not made over; left standing until it is applied again", family, version)
}

// converge applies family's newest version when the family serves the deployment
// that version's run was made over, and nothing else. A family serving another
// catalog (a new release of its file, or an edit made around this surface) is left
// standing: overwriting it would undo a deployment nobody here edited, so it serves
// the newest version again only when that is applied again.
func converge(ctx context.Context, family string) {
	// One step with any apply: an edit made between this read and this apply would
	// otherwise be written over by the version it replaced.
	routingMu.Lock()
	defer routingMu.Unlock()
	latest, err := object.LatestRoutingVersion(family)
	if err != nil || latest == nil {
		return
	}
	h, err := routingHost(family)
	if err != nil {
		return
	}
	snap, err := h.Snapshot(ctx)
	if err != nil {
		return
	}
	live, err := canonicalJSON(snap)
	if err != nil {
		return
	}
	if object.CatalogSum(live) == latest.Sum {
		return
	}
	d := deployment(live)
	if d != latest.Boot {
		leftStanding(family, latest.Id, d)
		return
	}
	if err := h.Apply(ctx, []byte(bare(latest.Catalog))); err != nil {
		unconverged[family] = d
		log.Warn("routing: %s did not take version %d: %v; an edit made from what it serves is let through", family, latest.Id, err)
		return
	}
	delete(unconverged, family)
	log.Info("routing: %s serves version %d again", family, latest.Id)
}
