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
	"github.com/zap-proto/zip"
	"strings"

	"github.com/hanzoai/ai/object"
)

// This file is the ONE declaration of the resource surface. Every CRUD route
// is generated from the table below — none are written by hand.
//
// The inherited surface was ~213 hand-written routes in flat verb-noun form,
// all at the top level:
//
//	/v1/get-users  /v1/get-user  /v1/add-user  /v1/update-user  /v1/delete-user
//
// Six lines per resource, thirty-odd resources, and the resource's identity was
// smeared across the verb of every one of them. Nothing said "users" in one
// place, so nothing could be reasoned about per-resource: a missing route, a
// wrong controller method, an unnamespaced collision were all invisible.
//
// The surface is now the resource, and the verb is the HTTP method where it
// belongs:
//
//	GET    /v1/ai/stores              list    POST   /v1/ai/stores              create
//	GET    /v1/ai/stores/:owner/:name  read    PATCH  /v1/ai/stores/:owner/:name update
//	                                           DELETE /v1/ai/stores/:owner/:name delete
//
// and every resource is namespaced by the subsystem that owns it, so the top
// level stays the small set of genuinely global endpoints (/v1/chat/completions,
// /v1/models, /v1/embeddings — the OpenAI-compatible surface) instead of being a
// dumping ground.
//
// WHAT THIS IS NOT: it is not an alias layer. The flat compound routes are gone,
// not deprecated — one way to reach a resource, and it is this one. The
// controller METHOD names (GetUsers/AddUser/…) are untouched; they are Go
// identifiers, an implementation detail of dispatch, and renaming ~200 methods
// would be churn with no user-visible meaning.
//
// The policy filters (authz, balance) keep keying on the same strings they
// always have — see policyKey below, which derives that key from this same
// table. One table, one truth, and no chance of a route existing whose
// permission the filters cannot name.

// resource is one REST resource: where it lives, and the controller method
// family it dispatches to.
type resource struct {
	// ns is the owning subsystem: the resource lives at /v1/<ns>/<path>.
	ns string
	// path is the plural URL segment, e.g. "users".
	path string
	// one is the singular controller method stem: "User" → GetUser/AddUser/…
	one string
	// shape is a zero value of what this resource IS, and it is the only place
	// the published response schema comes from. A collection answers a list of
	// it, a member answers one — [Document] reads the Go type's json tags rather
	// than restating them, so adding a field to the struct changes the contract
	// in the same commit. It sits beside `one` because they are two facts about
	// the same noun: what its handlers are called, and what it holds.
	shape any
	// many is the plural stem: "Users" → GetUsers. Set only when it is not
	// one+"s" (English is irregular: Activity→Activities).
	many string
	// readMethod overrides the member GET handler when it is not "Get"+one.
	// A handful of reads are scoped to the caller and named accordingly
	// (GetFileMy); the table records that rather than pretending otherwise.
	readMethod string
	// key / keyPlural are the nouns the OLD flat routes used, which the policy
	// maps are keyed by. Set them only where the URL noun differs from the flat
	// one — /v1/ai/routes was /v1/get-model-routes, so key is "model-route".
	// Everything else derives from path.
	key, keyPlural string
	// global adds GET /v1/<ns>/<path>/global → GetGlobal<many>, the cross-tenant
	// listing a handful of resources expose to admins. It is a sub-collection of
	// the resource rather than a separate top-level noun, and it is safe next to
	// /:id because the router prefers literal segments (see web.Router.match).
	global bool
	// noList / noRead / noCreate / noUpdate / noDelete drop a verb the resource
	// genuinely does not implement — the table stays honest about the surface
	// rather than registering a route to a method that does not exist.
	noList, noRead, noCreate, noUpdate, noDelete bool
	// actions are the non-CRUD operations, POSTed at
	// /v1/<ns>/<path>/<action> (collection) or /:id/<action> (member).
	actions []action
}

// action is one non-CRUD operation on a resource.
type action struct {
	// name is the URL segment: "vectors" → POST /v1/rag/stores/:id/vectors.
	name string
	// method is the controller method it dispatches to.
	method string
	// verb is the HTTP method; defaults to POST.
	verb string
	// collection puts the action on the collection rather than a member, i.e.
	// /v1/<ns>/<path>/<name> instead of /v1/<ns>/<path>/:id/<name>.
	collection bool
}

// resources is the whole CRUD surface. Adding a resource here is the only way to
// add one; there is no second registration path.
//
// The namespace is the SERVICE — `ai` — for every entry, per the canonical rule
// that a route is /v1/<service>/<resource>. The groupings below are how the
// table is read, not a second prefix:
//
//	identity   ai's own rows that inherited an identity noun from casibase
//	retrieval  the knowledge stores and what is indexed in them
//	chat       conversations and their messages
//	models     provider and route plumbing
//	content    authored things: articles, media, forms, templates
//	compute    the machines and what runs on them
//	work       tasks, workflows and their scales
//	ops        the operational record: audit, connections, activity, usage
var resources = []resource{
	// ── identity ── ai's own rows that inherited an identity noun from casibase.
	//
	// hanzoai/iam is the identity service and serves users, applications,
	// permissions, providers and sessions at /v1/iam/*; cloud proxies that WHOLE
	// subtree to it (cloud/iam_edge.go — `app.Group("/v1/iam").All("/*")`). ai
	// inherited the same five words for its own things, so each one had to be
	// answered separately: is this IAM's row reached through a second door, or
	// ai's own row wearing IAM's word?
	//
	// permissions was the first. Every handler in controllers/permission.go is an
	// iam.* call — the REST client in internal/iam, straight to the IAM server —
	// so /v1/ai/permissions was a second ADDRESS for /v1/iam/permissions, holding
	// no data of its own. It is gone; IAM's is the address.
	//
	// The others are ai's own rows in ai's own tables. They keep their routes
	// under a word that cannot be read as IAM's.

	// object.Application is a TEMPLATE rendered through kustomize into a manifest
	// and applied to a namespace, with a pod phase for Status and a service URL —
	// a deployment. It was never an OAuth client; only the word was IAM's.
	{ns: "ai", path: "deployments", one: "Application", shape: object.Application{}, key: "application", keyPlural: "applications",
		actions: []action{
			{name: "deploy", method: "DeployApplication"},
			{name: "undeploy", method: "UndeployApplication"},
		}},
	// ai's OWN cookie sessions: Signin writes the the router session id and Signout
	// deletes it (controllers/account.go). IAM cannot see them, so /v1/iam/sessions
	// is a different set of rows — which is exactly why the bare noun was wrong.
	//
	// readMethod is load-bearing: without it the member GET resolves to
	// "GetSession", which the embedded web.Controller ALSO defines as its
	// session-value getter. Reflection finds that one, so the table test passes,
	// and dispatch then calls a one-argument method with no arguments and panics.
	{ns: "ai", path: "signin-sessions", one: "Session", shape: object.Session{}, key: "session", keyPlural: "sessions",
		readMethod: "GetSingleSession",
		actions: []action{
			{name: "duplicated", method: "IsSessionDuplicated", verb: "GET", collection: true},
		}},

	// ── retrieval ── stores, their vectors, and the files indexed into them.
	{ns: "ai", path: "stores", one: "Store", shape: object.Store{}, global: true, actions: []action{
		{name: "vectors", method: "RefreshStoreVectors"},
		{name: "names", method: "GetStoreNames", verb: "GET", collection: true},
		{name: "providers", method: "GetStorageProviders", verb: "GET", collection: true},
	}},
	{ns: "ai", path: "vectors", one: "Vector", shape: object.Vector{}, global: true, actions: []action{
		{name: "all", method: "DeleteAllVectors", verb: "DELETE", collection: true},
	}},
	{ns: "ai", path: "files", one: "File", shape: object.File{}, readMethod: "GetFileMy", global: true, actions: []action{
		{name: "vectors", method: "RefreshFileVectors"},
		{name: "upload", method: "UploadFile", collection: true},
		// activate/active are a FILE CACHE (controllers/file_cache.go), keyed by
		// key+filename and prefix — not by a file's (owner, name). They take no id,
		// so they are collection actions; as member actions their route could never
		// match, because no caller has an id to put in it.
		{name: "activate", method: "ActivateFile", collection: true},
		{name: "active", method: "GetActiveFile", verb: "GET", collection: true},
	}},
	{ns: "ai", path: "tree-files", one: "TreeFile", shape: object.TreeFile{}, noList: true, noRead: true},

	// ── chat ── conversations and messages.
	{ns: "ai", path: "chats", one: "Chat", shape: object.Chat{}, global: true},
	{ns: "ai", path: "messages", one: "Message", shape: object.Message{}, global: true, actions: []action{
		{name: "answer", method: "GetMessageAnswer", verb: "GET"},
		{name: "welcome", method: "DeleteWelcomeMessage", verb: "DELETE", collection: true},
	}},

	// ── models ── provider and route plumbing.
	{ns: "ai", path: "providers", one: "Provider", shape: object.Provider{}, global: true, actions: []action{
		{name: "mcp-tools", method: "RefreshMcpTools", collection: true},
	}},
	{ns: "ai", path: "routes", one: "ModelRoute", shape: object.ModelRoute{}, key: "model-route", keyPlural: "model-routes"},

	// ── content ── authored things.
	{ns: "ai", path: "articles", one: "Article", shape: object.Article{}, global: true},
	{ns: "ai", path: "videos", one: "Video", shape: object.Video{}, global: true, actions: []action{
		{name: "upload", method: "UploadVideo", collection: true},
	}},
	{ns: "ai", path: "assets", one: "Asset", shape: object.Asset{}, actions: []action{
		{name: "scan", method: "ScanAsset"},
		{name: "scan", method: "ScanAssets", collection: true},
	}},
	{ns: "ai", path: "forms", one: "Form", shape: object.Form{}, global: true, actions: []action{
		{name: "data", method: "GetFormData", verb: "GET", collection: true},
	}},
	{ns: "ai", path: "templates", one: "Template", shape: object.Template{}},
	{ns: "ai", path: "graphs", one: "Graph", shape: object.Graph{}, global: true},

	// ── compute ── machines and what runs on them.
	{ns: "ai", path: "nodes", one: "Node", shape: object.Node{}, actions: []action{
		{name: "tunnel", method: "AddNodeTunnel"},
		{name: "tunnel", method: "GetNodeTunnel", verb: "GET"},
	}},
	{ns: "ai", path: "scans", one: "Scan", shape: object.Scan{}},

	// ── work ── tasks, workflows, scales.
	{ns: "ai", path: "tasks", one: "Task", shape: object.Task{}, global: true, actions: []action{
		{name: "document", method: "UploadTaskDocument"},
		{name: "analyze", method: "AnalyzeTask"},
	}},
	{ns: "ai", path: "workflows", one: "Workflow", shape: object.Workflow{}, global: true},
	{ns: "ai", path: "scales", one: "Scale", shape: object.Scale{}, global: true, actions: []action{
		{name: "public", method: "GetPublicScales", verb: "GET", collection: true},
	}},

	// ── ops ── the operational record.
	{ns: "ai", path: "records", one: "Record", shape: object.Record{}, actions: []action{
		{name: "batch", method: "AddRecords", collection: true},
		{name: "commit", method: "CommitRecord", collection: true},
		{name: "commit-second", method: "CommitRecordSecond", collection: true},
		{name: "query", method: "QueryRecord", verb: "GET", collection: true},
		{name: "query-second", method: "QueryRecordSecond", verb: "GET", collection: true},
	}},
	// NOT "connections": /v1/ai/connections is the AI Login Manager (org-scoped
	// logins to third-party AI accounts, router.go). These are the remote-access
	// connections the Guacamole surface drives — a different thing that happens to
	// share casibase's noun.
	{ns: "ai", path: "remote-connections", one: "Connection", shape: object.Connection{}, key: "connection", keyPlural: "connections", actions: []action{
		{name: "start", method: "StartConnection"},
		{name: "stop", method: "StopConnection"},
	}},
	{ns: "ai", path: "activities", one: "Activity", shape: object.Activity{}, many: "Activities",
		noRead: true, noCreate: true, noUpdate: true, noDelete: true},
	{ns: "ai", path: "usages", one: "Usage", shape: object.Usage{}, noRead: true, noCreate: true, noUpdate: true, noDelete: true,
		actions: []action{
			{name: "range", method: "GetRangeUsages", verb: "GET", collection: true},
			{name: "cloud", method: "GetCloudUsages", verb: "GET", collection: true},
			// Two cuts of this same record, both keyed by user, and neither one a
			// user directory: object.GetUsers reads the store's MESSAGES and returns
			// the distinct senders, GetUserTableInfos rolls those messages up into
			// per-user message/token/price rows. They were /v1/ai/users — a name that
			// promised IAM's users and delivered the usage panel's filter axis.
			{name: "user-names", method: "GetUsers", verb: "GET", collection: true},
			{name: "by-user", method: "GetUserTableInfos", verb: "GET", collection: true},
		}},
}

// singleton is an endpoint with no collection and no id — there is exactly one
// of it per caller or per deployment, so /:id would be meaningless. It gets the
// same namespacing as a resource and the same generated policy key; it simply
// has no member URL.
type singleton struct {
	ns   string
	path string
	// verbs maps HTTP method → controller method.
	verbs map[string]string
	// key is the flat name the policy maps use, e.g. "get-system-info". One per
	// method, because a singleton's GET and DELETE had different flat names.
	keys map[string]string
}

// singletons is every non-CRUD endpoint that used to sit at the top level in
// compound form. Same rule as resources: the noun is in the path, the verb is
// the HTTP method, and the subsystem owns the namespace.
var singletons = []singleton{
	// ai's own auth singletons. Under /v1/ai for the reason above: /v1/iam is
	// proxied wholesale to the IAM service, so signin registered there is dead.
	// These sign in TO ai — the OIDC exchange and the cookie session that
	// /v1/ai/signin-sessions records — and are ai's, not IAM's.
	{ns: "ai", path: "signin",
		verbs: map[string]string{"POST": "Signin"},
		keys:  map[string]string{"POST": "signin"}},
	{ns: "ai", path: "signout",
		verbs: map[string]string{"POST": "Signout"},
		keys:  map[string]string{"POST": "signout"}},
	{ns: "ai", path: "account",
		verbs: map[string]string{"GET": "GetAccount"},
		keys:  map[string]string{"GET": "get-account"}},
	{ns: "ai", path: "preferences",
		verbs: map[string]string{"PATCH": "UpdatePreferences", "PUT": "UpdatePreferences"},
		keys:  map[string]string{"PATCH": "update-preferences", "PUT": "update-preferences"}},

	{ns: "ai", path: "answer",
		verbs: map[string]string{"GET": "GetAnswer"},
		keys:  map[string]string{"GET": "get-answer"}},

	// Operational readouts about the deployment itself.
	{ns: "ai", path: "system",
		verbs: map[string]string{"GET": "GetSystemInfo"},
		keys:  map[string]string{"GET": "get-system-info"}},
	{ns: "ai", path: "version",
		verbs: map[string]string{"GET": "GetVersionInfo"},
		keys:  map[string]string{"GET": "get-version-info"}},
	{ns: "ai", path: "prometheus",
		verbs: map[string]string{"GET": "GetPrometheusInfo"},
		keys:  map[string]string{"GET": "get-prometheus-info"}},

	{ns: "ai", path: "k8s-status",
		verbs: map[string]string{"GET": "GetK8sStatus"},
		keys:  map[string]string{"GET": "get-k8s-status"}},
	{ns: "ai", path: "dashboards/vm",
		verbs: map[string]string{"GET": "GetVmDashboardUrl"},
		keys:  map[string]string{"GET": "get-vm-dashboard-url"}},
	{ns: "ai", path: "dashboards/agents",
		verbs: map[string]string{"GET": "GetAgentsDashboardUrl"},
		keys:  map[string]string{"GET": "get-agents-dashboard-url"}},

	// The caller's own routing history: read it or erase it. "my" needs no
	// saying — a caller can only ever reach its own.
	{ns: "router", path: "data",
		verbs: map[string]string{"GET": "ExportMyRoutingData", "DELETE": "DeleteMyRoutingData"},
		keys: map[string]string{"GET": "export-my-routing-data",
			"DELETE": "delete-my-routing-data"}},

	{ns: "ai", path: "training-contribution",
		verbs: map[string]string{"GET": "GetTrainingContribution",
			"PATCH": "UpdateTrainingContribution", "PUT": "UpdateTrainingContribution"},
		keys: map[string]string{"GET": "get-training-contribution",
			"PATCH": "update-training-contribution", "PUT": "update-training-contribution"}},
}

func (s singleton) url() string { return "/v1/" + s.ns + "/" + s.path }

// plural is the resource's plural controller stem.
func (r resource) plural() string {
	if r.many != "" {
		return r.many
	}
	return r.one + "s"
}

// collection is the resource's collection URL, /v1/<ns>/<path>.
func (r resource) collection() string { return "/v1/" + r.ns + "/" + r.path }

// member is the resource's member URL, /v1/<ns>/<path>/:owner/:name.
//
// The identity of every object in this system is the PAIR (owner, name) —
// object.GetStore and its siblings all begin by splitting a composite "owner/name"
// id. So the member URL carries both, which is both the honest REST spelling of a
// composite key and the only one that works: a single :id segment cannot hold a
// value with a slash in it (Go decodes %2F in URL.Path, so escaping does not
// help).
//
// It also makes the path shape unambiguous, which policyKey relies on: after
// /v1/<ns>/<path>, exactly two segments is a member, one segment is a
// collection action, and three is a member action. Nothing has to guess.
func (r resource) member() string { return r.collection() + "/:owner/:name" }

// registerResources binds every resource in the table. This is the only place
// CRUD routes are registered.
//
// One pattern dispatches to several methods via "VERB:Method" pairs, so a
// collection is one registration and a member is another — five routes per
// resource collapse to two patterns.
//
// These patterns are the router's, and they stay the router's. A ZAP caller
// reaches them through this same table: a gateway request that no fast-path
// prefix claims is replayed through the router with its method and its
// :owner/:name intact. Putting a prefix in front of a resource instead is not an
// optimisation that is available — a matcher that sees only the path cannot tell
// a member's four verbs apart, nor a member from its collection.
// controllers/zap_gateway_fallback.go carries that argument in full; this is the
// note for anyone who counts the handlers, finds no prefix for the resources,
// and reads the absence as unfinished work. It is the design.
func registerResources(app *zip.App) {
	for _, r := range resources {
		if spec := collectionSpec(r); spec != "" {
			route(app, r.collection(), spec)
		}
		if spec := memberSpec(r); spec != "" {
			route(app, r.member(), spec)
		}
		if r.global {
			route(app, r.collection()+"/global", "GET:GetGlobal"+r.plural())
		}
		// Actions that share a URL must be registered as ONE pattern with several
		// verb:method pairs. Registering the same pattern twice leaves the second
		// registration unreachable — GET and POST on /nodes/:id/tunnel are the
		// live example — and it fails as a 405, not an error, so nothing complains.
		order := []string{}
		byPath := map[string][]string{}
		for _, a := range r.actions {
			verb := a.verb
			if verb == "" {
				verb = "POST"
			}
			p := r.member() + "/" + a.name
			if a.collection {
				p = r.collection() + "/" + a.name
			}
			if _, seen := byPath[p]; !seen {
				order = append(order, p)
			}
			byPath[p] = append(byPath[p], verb+":"+a.method)
		}
		for _, p := range order {
			route(app, p, strings.Join(byPath[p], ";"))
		}
	}
	for _, s := range singletons {
		var parts []string
		for _, verb := range []string{"GET", "POST", "PATCH", "PUT", "DELETE"} {
			if m, ok := s.verbs[verb]; ok {
				parts = append(parts, verb+":"+m)
			}
		}
		route(app, s.url(), strings.Join(parts, ";"))
	}
}

// collectionSpec is the "GET:List;POST:Create" pair for /v1/<ns>/<path>.
func collectionSpec(r resource) string {
	var parts []string
	if !r.noList {
		parts = append(parts, "GET:Get"+r.plural())
	}
	if !r.noCreate {
		parts = append(parts, "POST:Add"+r.one)
	}
	return strings.Join(parts, ";")
}

// memberSpec is the "GET:Read;PATCH:Update;DELETE:Delete" set for the member URL.
//
// PUT is accepted alongside PATCH: a full replacement and a partial update reach
// the same handler because the handler has always taken a whole object. Refusing
// PUT would be a distinction the implementation does not actually make.
func memberSpec(r resource) string {
	var parts []string
	if !r.noRead {
		read := r.readMethod
		if read == "" {
			read = "Get" + r.one
		}
		parts = append(parts, "GET:"+read)
	}
	if !r.noUpdate {
		parts = append(parts, "PATCH:Update"+r.one, "PUT:Update"+r.one)
	}
	if !r.noDelete {
		parts = append(parts, "DELETE:Delete"+r.one)
	}
	return strings.Join(parts, ";")
}

// policyKey maps a live request (REST path + method) back to the string the
// authz and balance filters have always keyed on — "get-users", "add-chat", and
// so on.
//
// This is why the rename could not introduce an auth hole. Those filters hold
// hand-curated policy sets (super-admin-only endpoints, balance exemptions,
// demo-mode allowances) keyed by the old names. Rewriting ~200 entries across
// several maps by hand is exactly the edit where one silently-missed line means
// an admin endpoint answering an anonymous caller. Instead the SAME table that
// generates the routes generates the key, so a policy entry cannot drift from
// the route it governs: if a resource is in the table, its key is derivable, and
// if it is not, nothing about it changed.
//
// Returns "" when the path is not a table-generated resource route, which tells
// the caller to fall back to its existing behaviour (the top-level endpoints,
// whose names never changed).
func policyKey(path, method string) string {
	seg := strings.Split(strings.Trim(strings.TrimPrefix(path, "/v1/"), "/"), "/")
	if len(seg) < 2 {
		return ""
	}
	for _, sg := range singletons {
		if sg.ns == seg[0] && sg.path == seg[1] && len(seg) == 2 {
			return sg.keys[strings.ToUpper(method)]
		}
	}
	r, ok := lookup(seg[0], seg[1])
	if !ok {
		return ""
	}
	// A member is exactly (owner, name), so the tail length says which shape this
	// is with no guessing: 0 = collection, 1 = collection action, 2 = member,
	// 3 = member action.
	switch tail := seg[2:]; len(tail) {
	case 0:
		return collectionKey(r, method)
	case 1:
		if r.global && tail[0] == "global" && strings.EqualFold(method, "GET") {
			return "get-global-" + pluralNoun(r)
		}
		return actionKey(r, tail[0], method, true)
	case 2:
		return memberKey(r, method)
	case 3:
		return actionKey(r, tail[2], method, false)
	}
	return ""
}

func collectionKey(r resource, method string) string {
	switch strings.ToUpper(method) {
	case "GET":
		return "get-" + pluralNoun(r)
	case "POST":
		return "add-" + singularNoun(r)
	}
	return ""
}

func memberKey(r resource, method string) string {
	switch strings.ToUpper(method) {
	case "GET":
		return "get-" + singularNoun(r)
	case "PATCH", "PUT":
		return "update-" + singularNoun(r)
	case "DELETE":
		return "delete-" + singularNoun(r)
	}
	return ""
}

// actionKey names an action with the same shape the old flat route used, so a
// policy entry like "start-connection" keeps matching.
func actionKey(r resource, name, method string, onCollection bool) string {
	for _, a := range r.actions {
		verb := a.verb
		if verb == "" {
			verb = "POST"
		}
		if a.name == name && a.collection == onCollection && strings.EqualFold(verb, method) {
			return kebab(a.method)
		}
	}
	return ""
}

// singularNoun / pluralNoun are the nouns the policy maps are keyed by. They
// default to the URL path, and are overridden where the two genuinely differ.
func singularNoun(r resource) string {
	if r.key != "" {
		return r.key
	}
	if strings.HasSuffix(r.path, "ies") {
		return strings.TrimSuffix(r.path, "ies") + "y"
	}
	return strings.TrimSuffix(r.path, "s")
}

func pluralNoun(r resource) string {
	if r.keyPlural != "" {
		return r.keyPlural
	}
	return r.path
}

// lookup finds the resource at /v1/<ns>/<path>.
func lookup(ns, path string) (resource, bool) {
	for _, r := range resources {
		if r.ns == ns && r.path == path {
			return r, true
		}
	}
	return resource{}, false
}

// kebab turns a controller method name into the flat-route spelling the policy
// maps use: RefreshStoreVectors → refresh-store-vectors.
func kebab(method string) string {
	var b strings.Builder
	for i, c := range method {
		if c >= 'A' && c <= 'Z' {
			if i > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(c - 'A' + 'a')
			continue
		}
		b.WriteRune(c)
	}
	return b.String()
}
