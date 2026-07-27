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
	"strings"

	"github.com/hanzoai/ai/controllers"
	"github.com/hanzoai/ai/web"
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
//	GET    /v1/iam/users        list        POST   /v1/iam/users       create
//	GET    /v1/iam/users/:id    read        PATCH  /v1/iam/users/:id   update
//	                                        DELETE /v1/iam/users/:id   delete
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
// Namespaces are the subsystems that own the data, so a reader can tell what a
// route touches from its prefix alone:
//
//	iam      identity: who exists, what they may do
//	rag      retrieval: the knowledge stores and what is indexed in them
//	chat     conversations and their messages
//	ai       model plumbing: providers and routes
//	content  authored things: articles, media, forms, templates
//	compute  the machines and what runs on them
//	work     tasks, workflows and their scales
//	ops      the operational record: audit, connections, activity, usage
var resources = []resource{
	// ── iam ── identity and access.
	{ns: "iam", path: "users", one: "User", noRead: true, noCreate: true, noUpdate: true, noDelete: true,
		actions: []action{{name: "table-infos", method: "GetUserTableInfos", verb: "GET", collection: true}}},
	{ns: "iam", path: "applications", one: "Application", actions: []action{
		{name: "deploy", method: "DeployApplication"},
		{name: "undeploy", method: "UndeployApplication"},
	}},
	{ns: "iam", path: "permissions", one: "Permission"},
	{ns: "iam", path: "sessions", one: "Session", actions: []action{
		{name: "duplicated", method: "IsSessionDuplicated", verb: "GET", collection: true},
	}},

	// ── rag ── retrieval: stores, their vectors, and the files indexed into them.
	{ns: "rag", path: "stores", one: "Store", global: true, actions: []action{
		{name: "vectors", method: "RefreshStoreVectors"},
		{name: "names", method: "GetStoreNames", verb: "GET", collection: true},
		{name: "providers", method: "GetStorageProviders", verb: "GET", collection: true},
	}},
	{ns: "rag", path: "vectors", one: "Vector", global: true, actions: []action{
		{name: "all", method: "DeleteAllVectors", verb: "DELETE", collection: true},
	}},
	{ns: "rag", path: "files", one: "File", readMethod: "GetFileMy", global: true, actions: []action{
		{name: "vectors", method: "RefreshFileVectors"},
		{name: "upload", method: "UploadFile", collection: true},
		{name: "activate", method: "ActivateFile"},
		{name: "active", method: "GetActiveFile", verb: "GET", collection: true},
	}},
	{ns: "rag", path: "tree-files", one: "TreeFile", noList: true, noRead: true},

	// ── chat ── conversations and messages.
	{ns: "chat", path: "chats", one: "Chat", global: true},
	{ns: "chat", path: "messages", one: "Message", global: true, actions: []action{
		{name: "answer", method: "GetMessageAnswer", verb: "GET"},
		{name: "welcome", method: "DeleteWelcomeMessage", verb: "DELETE", collection: true},
	}},

	// ── ai ── model plumbing.
	{ns: "ai", path: "providers", one: "Provider", global: true, actions: []action{
		{name: "mcp-tools", method: "RefreshMcpTools", collection: true},
	}},
	{ns: "ai", path: "routes", one: "ModelRoute", key: "model-route", keyPlural: "model-routes"},

	// ── content ── authored things.
	{ns: "content", path: "articles", one: "Article", global: true},
	{ns: "content", path: "videos", one: "Video", global: true, actions: []action{
		{name: "upload", method: "UploadVideo", collection: true},
	}},
	{ns: "content", path: "assets", one: "Asset", actions: []action{
		{name: "scan", method: "ScanAsset"},
		{name: "scan", method: "ScanAssets", collection: true},
	}},
	{ns: "content", path: "forms", one: "Form", global: true, actions: []action{
		{name: "data", method: "GetFormData", verb: "GET", collection: true},
	}},
	{ns: "content", path: "templates", one: "Template"},
	{ns: "content", path: "graphs", one: "Graph", global: true},

	// ── compute ── machines and what runs on them.
	{ns: "compute", path: "nodes", one: "Node", actions: []action{
		{name: "tunnel", method: "AddNodeTunnel"},
		{name: "tunnel", method: "GetNodeTunnel", verb: "GET"},
	}},
	{ns: "compute", path: "scans", one: "Scan"},

	// ── work ── tasks, workflows, scales.
	{ns: "work", path: "tasks", one: "Task", global: true, actions: []action{
		{name: "document", method: "UploadTaskDocument"},
		{name: "analyze", method: "AnalyzeTask"},
	}},
	{ns: "work", path: "workflows", one: "Workflow", global: true},
	{ns: "work", path: "scales", one: "Scale", global: true, actions: []action{
		{name: "public", method: "GetPublicScales", verb: "GET", collection: true},
	}},

	// ── ops ── the operational record.
	{ns: "ops", path: "records", one: "Record", actions: []action{
		{name: "batch", method: "AddRecords", collection: true},
		{name: "commit", method: "CommitRecord", collection: true},
		{name: "commit-second", method: "CommitRecordSecond", collection: true},
		{name: "query", method: "QueryRecord", verb: "GET", collection: true},
		{name: "query-second", method: "QueryRecordSecond", verb: "GET", collection: true},
	}},
	{ns: "ops", path: "connections", one: "Connection", actions: []action{
		{name: "start", method: "StartConnection"},
		{name: "stop", method: "StopConnection"},
	}},
	{ns: "ops", path: "activities", one: "Activity", many: "Activities",
		noRead: true, noCreate: true, noUpdate: true, noDelete: true},
	{ns: "ops", path: "usages", one: "Usage", noRead: true, noCreate: true, noUpdate: true, noDelete: true,
		actions: []action{
			{name: "range", method: "GetRangeUsages", verb: "GET", collection: true},
			{name: "cloud", method: "GetCloudUsages", verb: "GET", collection: true},
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
	// Auth/account. These already answered at /v1/iam/* via a rewrite filter;
	// now they simply live there, and the rewrite is gone.
	{ns: "iam", path: "signin",
		verbs: map[string]string{"POST": "Signin"},
		keys:  map[string]string{"POST": "signin"}},
	{ns: "iam", path: "signout",
		verbs: map[string]string{"POST": "Signout"},
		keys:  map[string]string{"POST": "signout"}},
	{ns: "iam", path: "account",
		verbs: map[string]string{"GET": "GetAccount"},
		keys:  map[string]string{"GET": "get-account"}},
	{ns: "iam", path: "preferences",
		verbs: map[string]string{"PATCH": "UpdatePreferences", "PUT": "UpdatePreferences"},
		keys:  map[string]string{"PATCH": "update-preferences", "PUT": "update-preferences"}},

	{ns: "chat", path: "answer",
		verbs: map[string]string{"GET": "GetAnswer"},
		keys:  map[string]string{"GET": "get-answer"}},

	// Operational readouts about the deployment itself.
	{ns: "ops", path: "system",
		verbs: map[string]string{"GET": "GetSystemInfo"},
		keys:  map[string]string{"GET": "get-system-info"}},
	{ns: "ops", path: "version",
		verbs: map[string]string{"GET": "GetVersionInfo"},
		keys:  map[string]string{"GET": "get-version-info"}},
	{ns: "ops", path: "prometheus",
		verbs: map[string]string{"GET": "GetPrometheusInfo"},
		keys:  map[string]string{"GET": "get-prometheus-info"}},

	{ns: "compute", path: "k8s",
		verbs: map[string]string{"GET": "GetK8sStatus"},
		keys:  map[string]string{"GET": "get-k8s-status"}},
	{ns: "compute", path: "vm-dashboard",
		verbs: map[string]string{"GET": "GetVmDashboardUrl"},
		keys:  map[string]string{"GET": "get-vm-dashboard-url"}},
	{ns: "agents", path: "dashboard",
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

// member is the resource's member URL, /v1/<ns>/<path>/:id.
func (r resource) member() string { return r.collection() + "/:id" }

// registerResources binds every resource in the table. This is the only place
// CRUD routes are registered.
//
// Beego dispatches one pattern to several methods via "VERB:Method" pairs, so a
// collection is one registration and a member is another — five routes per
// resource collapse to two patterns.
func registerResources(app *web.Router) {
	for _, r := range resources {
		if spec := collectionSpec(r); spec != "" {
			app.Router(r.collection(), &controllers.ApiController{}, spec)
		}
		if spec := memberSpec(r); spec != "" {
			app.Router(r.member(), &controllers.ApiController{}, spec)
		}
		if r.global {
			app.Router(r.collection()+"/global", &controllers.ApiController{}, "GET:GetGlobal"+r.plural())
		}
		for _, a := range r.actions {
			verb := a.verb
			if verb == "" {
				verb = "POST"
			}
			path := r.member() + "/" + a.name
			if a.collection {
				path = r.collection() + "/" + a.name
			}
			app.Router(path, &controllers.ApiController{}, verb+":"+a.method)
		}
	}
	for _, s := range singletons {
		var parts []string
		for _, verb := range []string{"GET", "POST", "PATCH", "PUT", "DELETE"} {
			if m, ok := s.verbs[verb]; ok {
				parts = append(parts, verb+":"+m)
			}
		}
		app.Router(s.url(), &controllers.ApiController{}, strings.Join(parts, ";"))
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
	for _, s := range singletons {
		if s.ns == seg[0] && s.path == seg[1] && len(seg) == 2 {
			return s.keys[strings.ToUpper(method)]
		}
	}
	r, ok := lookup(seg[0], seg[1])
	if !ok {
		return ""
	}
	// /v1/<ns>/<path>/<tail>… — an action, or a member verb.
	tail := seg[2:]
	if len(tail) == 0 {
		return collectionKey(r, method)
	}
	if r.global && len(tail) == 1 && tail[0] == "global" && strings.EqualFold(method, "GET") {
		return "get-global-" + pluralNoun(r)
	}
	// A member action is /:id/<name>; a collection action is /<name>.
	if name := tail[len(tail)-1]; len(tail) > 1 || !isMemberVerb(method) {
		if key := actionKey(r, name, method); key != "" {
			return key
		}
	}
	if len(tail) == 1 {
		if key := actionKey(r, tail[0], method); key != "" {
			return key
		}
		return memberKey(r, method)
	}
	return ""
}

// isMemberVerb reports whether the method addresses a member (…/:id) rather than
// naming an action on the collection.
func isMemberVerb(method string) bool {
	switch strings.ToUpper(method) {
	case "GET", "PATCH", "PUT", "DELETE":
		return true
	}
	return false
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
func actionKey(r resource, name, method string) string {
	for _, a := range r.actions {
		verb := a.verb
		if verb == "" {
			verb = "POST"
		}
		if a.name == name && strings.EqualFold(verb, method) {
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
