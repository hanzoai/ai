// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2023 The OpenAgent Authors. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package object

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/util"
)

func InitDb() {
	modelProviderName, embeddingProviderName, ttsProviderName, sttProviderName := initBuiltInProviders()
	initLLMProviders()
	initBuiltInStore(modelProviderName, embeddingProviderName, ttsProviderName, sttProviderName)
	initTemplates()
	initModelAccessSeed()
}

// initModelAccessSeed grants the Enso limited-preview SKUs to the launch org so its
// accounts work immediately. The grant is ORG-WIDE (User=""), so every member of the
// `hanzo` org — including z (owner=hanzo, name=z) — is granted without enumerating
// users. Idempotent: UpsertModelAccess never downgrades a grant, so re-running at
// every boot is a no-op. Widen access later via the SuperAdmin grant endpoint — this
// seed is only the founding allowlist, not the policy.
func initModelAccessSeed() {
	const org = "hanzo"
	for _, m := range []string{"enso", "enso-ultra"} {
		if _, err := GrantModelAccess(org, "", "", m); err != nil {
			fmt.Printf("initModelAccessSeed: org-grant %s %s failed: %v\n", org, m, err)
		}
	}
}

func initBuiltInStore(modelProviderName string, embeddingProviderName string, ttsProviderName string, sttProviderName string) {
	stores, err := GetGlobalStores()
	if err != nil {
		panic(err)
	}
	if len(stores) > 0 {
		return
	}
	imageProviderName := ""
	providerDbName := conf.GetConfigString("providerDbName")
	if providerDbName != "" {
		imageProviderName = "provider_storage_hanzo_default"
	}
	store := &Store{
		Owner:                "admin",
		Name:                 "store-built-in",
		CreatedTime:          util.GetCurrentTime(),
		DisplayName:          "Built-in Store",
		Title:                "AI Assistant",
		Avatar:               "https://cdn.hanzo.ai/static/favicon.png",
		StorageProvider:      "provider-storage-built-in",
		StorageSubpath:       "store-built-in",
		ImageProvider:        imageProviderName,
		SplitProvider:        "Default",
		ModelProvider:        modelProviderName,
		EmbeddingProvider:    embeddingProviderName,
		AgentProvider:        "",
		TextToSpeechProvider: ttsProviderName,
		SpeechToTextProvider: sttProviderName,
		Frequency:            10000,
		MemoryLimit:          10,
		LimitMinutes:         15,
		Welcome:              "Hello",
		WelcomeTitle:         "Hello, this is the Hanzo AI Assistant",
		WelcomeText:          "I'm here to help answer your questions",
		Prompt:               "You are an expert in your field and you specialize in using your knowledge to answer or solve people's problems.",
		ExampleQuestions:     []ExampleQuestion{},
		KnowledgeCount:       5,
		SuggestionCount:      3,
		ThemeColor:           "#5734d3",
		ChildStores:          []string{},
		ChildModelProviders:  []string{},
		IsDefault:            true,
		State:                "Active",
		PropertiesMap:        map[string]*Properties{},
	}
	if providerDbName != "" {
		store.ShowAutoRead = true
		store.DisableFileUpload = true
		tokens := conf.ReadGlobalConfigTokens()
		if len(tokens) > 0 {
			store.Title = tokens[0]
			store.Avatar = tokens[1]
			store.Welcome = tokens[2]
			store.WelcomeTitle = tokens[3]
			store.WelcomeText = tokens[4]
			store.Prompt = tokens[5]
		}
	}
	_, err = AddStore(store)
	if err != nil {
		panic(err)
	}
}

func getDefaultStoragePath() (string, error) {
	providerDbName := conf.GetConfigString("providerDbName")
	if providerDbName != "" {
		dbName := conf.GetConfigString("dbName")
		return fmt.Sprintf("C:/hanzo_cloud_data/%s", dbName), nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	res := filepath.Join(cwd, "files")
	return res, nil
}

func initBuiltInProviders() (string, string, string, string) {
	storageProvider, err := GetDefaultStorageProvider()
	if err != nil {
		panic(err)
	}
	modelProvider, err := GetDefaultModelProvider()
	if err != nil {
		panic(err)
	}
	embeddingProvider, err := GetDefaultEmbeddingProvider()
	if err != nil {
		panic(err)
	}
	ttsProvider, err := GetDefaultTextToSpeechProvider()
	if err != nil {
		panic(err)
	}
	if storageProvider == nil {
		var path string
		path, err = getDefaultStoragePath()
		if err != nil {
			panic(err)
		}
		util.EnsureFileFolderExists(path)
		storageProvider = &Provider{
			Owner:       "admin",
			Name:        "provider-storage-built-in",
			CreatedTime: util.GetCurrentTime(),
			DisplayName: "Built-in Storage Provider",
			Category:    "Storage",
			Type:        "Local File System",
			ClientId:    path,
			IsDefault:   true,
		}
		_, err = AddProvider(storageProvider)
		if err != nil && !isDuplicateKeyErr(err) {
			panic(err)
		}
	}
	if modelProvider == nil {
		modelProvider = &Provider{
			Owner:       "admin",
			Name:        "dummy-model-provider",
			CreatedTime: util.GetCurrentTime(),
			DisplayName: "Dummy Model Provider",
			Category:    "Model",
			Type:        "Dummy",
			SubType:     "Dummy",
			IsDefault:   true,
		}
		_, err = AddProvider(modelProvider)
		if err != nil && !isDuplicateKeyErr(err) {
			panic(err)
		}
	}
	// Real default embedding provider so RAG ingest actually produces vectors. A
	// "Dummy" provider accepts files but embeds nothing (documentsIndexed:0), which
	// silently broke ingest.
	//
	// The embedder MUST call an OpenAI-compatible endpoint the cloud pod reaches
	// WITHOUT a multi-minute hang. Type "OpenAI" with an EMPTY ProviderUrl targets
	// api.openai.com directly, and a server-side (in-cluster) embed to that host
	// crawls ~180s and then fails — so RAG ingest took ~180-210s AND the vector leg
	// never persisted (writeDocsToVector's sample embed timed out before
	// ensureVectorCollection ran, so the Qdrant collection was never created;
	// root-caused 2026-07-04). The fix: point the default embedder at the HANZO
	// GATEWAY (the SAME base + key the cloud AI client uses — CLOUD_AI_BASE_URL /
	// CLOUD_AI_API_KEY, one config, no drift), which serves embeddings in <1s and
	// normalizes every model to KB_EMBED_DIMS (1024). SubType text-embedding-qwen3
	// is the Hanzo-native, in-catalog embedder (routes to do-ai qwen3-embedding).
	// Operators can still repoint this default at a self-hosted embedder via the
	// console; the heal below only rewrites the two KNOWN-BROKEN defaults (Dummy,
	// or an api.openai.com-direct URL), never an operator's intentional config.
	realEmbed := func(p *Provider) {
		p.DisplayName = "Hanzo Embeddings"
		p.Category = "Embedding"
		p.Type = "OpenAI"
		p.SubType = defaultEmbedModel()
		p.ClientSecret = "kms://" + defaultEmbedKeyRef()
		p.ProviderUrl = defaultEmbedBaseURL()
		p.State = "Active"
	}
	switch {
	case embeddingProvider == nil:
		embeddingProvider = &Provider{Owner: "admin", Name: "default-embed", CreatedTime: util.GetCurrentTime(), IsDefault: true}
		realEmbed(embeddingProvider)
		_, err = AddProvider(embeddingProvider)
		if err != nil && !isDuplicateKeyErr(err) {
			panic(err)
		}
	case embedderNeedsHeal(embeddingProvider):
		// Self-heal the legacy Dummy default AND the external-OpenAI-direct default
		// (empty/api.openai.com ProviderUrl — the ~180s in-cluster hang) IN PLACE
		// (keep Name + IsDefault) so existing deployments converge to the gateway on
		// the next boot. An operator's intentional self-hosted repoint is left alone.
		realEmbed(embeddingProvider)
		if _, uerr := UpdateProvider("admin/"+embeddingProvider.Name, embeddingProvider); uerr != nil {
			fmt.Printf("[init] WARNING: failed to upgrade embedding provider %q to a real embedder: %v\n", embeddingProvider.Name, uerr)
		}
	}
	ttsProviderName := "Browser Built-In"
	if ttsProvider != nil {
		ttsProviderName = ttsProvider.Name
	}
	sttProviderName := "Browser Built-In"
	return modelProvider.Name, embeddingProvider.Name, ttsProviderName, sttProviderName
}

// defaultEmbedBaseURL is the OpenAI-compatible base the default RAG embedder
// calls. It MUST be a URL the cloud pod reaches without a multi-minute hang: the
// Hanzo gateway, NOT api.openai.com. Reuses the SAME base the cloud AI client
// uses (CLOUD_AI_BASE_URL) so there is one gateway config, no drift;
// HANZO_EMBED_BASE_URL overrides for a bespoke embedder host.
func defaultEmbedBaseURL() string {
	for _, k := range []string{"HANZO_EMBED_BASE_URL", "CLOUD_AI_BASE_URL"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return strings.TrimRight(v, "/")
		}
	}
	return "https://api.hanzo.ai/v1"
}

// defaultEmbedModel is the gateway embedding model the default embedder requests.
// The gateway normalizes every embedding model to KB_EMBED_DIMS (1024);
// text-embedding-qwen3 is the Hanzo-native, in-catalog embedder.
func defaultEmbedModel() string {
	if v := strings.TrimSpace(os.Getenv("HANZO_EMBED_MODEL")); v != "" {
		return v
	}
	return "text-embedding-qwen3"
}

// defaultEmbedKeyRef is the env var (a kms:// reference name) holding the gateway
// key the embedder authenticates with — the SAME static key the cloud AI client
// presents. ResolveProviderSecret resolves it env-first.
func defaultEmbedKeyRef() string {
	if v := strings.TrimSpace(os.Getenv("HANZO_EMBED_KEY_REF")); v != "" {
		return v
	}
	return "CLOUD_AI_API_KEY"
}

// embedderNeedsHeal reports whether the existing default embedding provider is one
// of the two KNOWN-BROKEN defaults that must be rewritten to the gateway on boot:
// the legacy "Dummy" (embeds nothing) or an api.openai.com-direct config (empty or
// openai.com ProviderUrl) that hangs ~180s in-cluster. An operator's intentional
// self-hosted repoint (any other non-empty, non-openai.com URL) is left untouched.
func embedderNeedsHeal(p *Provider) bool {
	if p == nil || p.Type == "Dummy" {
		return true
	}
	u := strings.ToLower(strings.TrimSpace(p.ProviderUrl))
	return u == "" || strings.Contains(u, "openai.com")
}

// embedGatewayBaseIfConfigured returns an EXPLICITLY-configured gateway base
// (HANZO_EMBED_BASE_URL, else CLOUD_AI_BASE_URL) or "" if neither is set. Unlike
// defaultEmbedBaseURL it has NO hardcoded fallback: the resolution-time override
// (forceGatewayEmbedder) only fires when an operator has actually wired a gateway
// base, so a standalone ai deployment with no gateway env is never redirected.
func embedGatewayBaseIfConfigured() string {
	for _, k := range []string{"HANZO_EMBED_BASE_URL", "CLOUD_AI_BASE_URL"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return strings.TrimRight(v, "/")
		}
	}
	return ""
}

// forceGatewayEmbedder redirects a known-broken default embedder (the
// api.openai.com-direct config that hangs ~180s from in-cluster) to the
// configured Hanzo gateway, IN PLACE, at embedder-resolution time. It is the
// runtime counterpart of the InitDb seed heal: the seed fixes the DB record for
// deployments that run InitDb, while this catches the embed path in the fused
// cloud binary (which reads the persisted DB provider and does not re-seed it),
// so it works no matter which record the default store resolves. No-op unless a
// gateway base is configured AND the provider is one of the known-broken defaults
// — a healthy custom embedder is never touched.
func forceGatewayEmbedder(p *Provider) {
	if p == nil || !embedderNeedsHeal(p) {
		return
	}
	base := embedGatewayBaseIfConfigured()
	if base == "" {
		return
	}
	p.Type = "OpenAI"
	p.ProviderUrl = base
	p.SubType = defaultEmbedModel()
	// Resolve the gateway key here (env-first, exactly as ResolveProviderSecret
	// does) rather than leaving a kms:// ref: this override runs at resolution
	// time, possibly AFTER the secret was already resolved for this *Provider, so
	// a bare ref would reach the HTTP client unresolved (→ 401). Fall back to the
	// kms:// ref only when the env var is unset (a real KMS resolves it downstream).
	keyRef := defaultEmbedKeyRef()
	if v := strings.TrimSpace(os.Getenv(keyRef)); v != "" {
		p.ClientSecret = v
	} else {
		p.ClientSecret = "kms://" + keyRef
	}
}

// seededLLMProviders is the LLM provider seed table: the records the model
// routing table needs (see controllers/model_routes.go), each mapping to an
// upstream service with its own API key and base URL. Secrets may be KMS
// references ("kms://SECRET_NAME"), resolved at call time by
// ResolveProviderSecret.
//
// DO-first defaults: do-ai is the primary (State Active, IsDefault true) —
// the universal DigitalOcean GenAI router that backs OpenAI/Anthropic/Llama/
// DeepSeek/Qwen/GLM/Kimi. fireworks and openai-direct ship DISABLED (an admin
// opts in via /v1/admin/providers/toggle). openrouter ships DISABLED
// (toggleable), keeping "DO-first" the default while making the catalog
// manageable in the same table. zen is deliberately absent: it is a model
// FAMILY whose address is deployment config (ZEN_URL), exactly like its sibling
// enso — see pruneStaleZenSeed for what a row here does to it.
//
// It lives at package scope so the invariants it must satisfy are testable: a
// seeded FAMILY row carrying a trailing /v1 is the defect that broke zen, and it
// is invisible at runtime, so it is asserted in a test instead (see
// SeededModelProviders and controllers/provider_seed_test.go).
var seededLLMProviders = []Provider{
	{
		Owner:        "admin",
		Name:         "do-ai",
		DisplayName:  "DigitalOcean AI (GenAI)",
		Category:     "Model",
		Type:         "DigitalOcean",
		SubType:      "gpt-4o",
		ProviderUrl:  "https://inference.do-ai.run/v1",
		ClientSecret: "kms://DO_AI_API_KEY",
		State:        "Active",
		IsDefault:    true, // primary router (DO-first)
	},
	{
		Owner:        "admin",
		Name:         "fireworks",
		DisplayName:  "Fireworks AI",
		Category:     "Model",
		Type:         "Fireworks",
		SubType:      "accounts/fireworks/models/deepseek-v3p2",
		ProviderUrl:  "https://api.fireworks.ai/inference/v1",
		ClientSecret: "kms://FIREWORKS_API_KEY",
		State:        "Disabled", // opt-in via /v1/admin/providers/toggle
		IsDefault:    false,
	},
	{
		Owner:        "admin",
		Name:         "openai-direct",
		DisplayName:  "OpenAI Direct",
		Category:     "Model",
		Type:         "OpenAI",
		SubType:      "gpt-5",
		ProviderUrl:  "https://api.openai.com/v1",
		ClientSecret: "kms://OPENAI_API_KEY",
		State:        "Disabled", // opt-in via /v1/admin/providers/toggle
		IsDefault:    false,
	},
	{
		Owner:       "admin",
		Name:        "openrouter",
		DisplayName: "OpenRouter",
		Category:    "Model",
		Type:        "OpenRouter",
		SubType:     "openrouter/auto",
		// NO trailing /v1 — openrouter is a model FAMILY, and both family paths
		// append it themselves (discovery does base+"/v1/models", pipeToFamily
		// does ProviderUrl+"/v1/"+apiPath). The direct-relay providers above
		// carry /v1 because nothing appends it for them. Seeded with /v1 here,
		// every call would go to .../api/v1/v1/... and 404 — invisible until the
		// row was toggled on, since nothing read it before.
		ProviderUrl:  "https://openrouter.ai/api",
		ClientSecret: "kms://OPENROUTER_API_KEY",
		State:        "Disabled", // DO-first: off by default, toggleable
		IsDefault:    false,
	},
	{
		// Zen video served on OUR GB10 (spark) via Hanzo Studio. The
		// zen3-video* routes (controllers/model_routes.go) resolve to this row;
		// videos_api.go drives the SAME OpenAI Sora-style async /v1/videos API
		// as do-ai (create → poll → download) against ProviderUrl. Type
		// DigitalOcean reuses the custom-URL branch in resolveEndpointForPath so
		// videoUpstreamBase yields the clean /v1 base. The zen3-video family is
		// owned_by hanzo; the public owner travels in owned_by (hip-00NN).
		Owner:        "admin",
		Name:         "spark-video",
		DisplayName:  "Hanzo Spark Video (GB10)",
		Category:     "Model",
		Type:         "DigitalOcean",
		SubType:      "wan2-2-t2v-a14b",
		ProviderUrl:  "https://spark-video.hanzo.ai/v1",
		ClientSecret: "kms://SPARK_VIDEO_API_KEY",
		State:        "Active", // first-party video family — keep on
		IsDefault:    false,
	},
}

// SeededModelProviders returns the seed table projected to name→ProviderUrl.
//
// Exported for the SAME reason FamilyProviderNames is: so the invariants this
// table must satisfy can be asserted from a package whose suite actually runs.
// object's own TestMain exits before m.Run() when no seeded database is present,
// so a guard living here would be inert — false assurance, which is worse than
// no guard. See controllers/provider_seed_test.go.
func SeededModelProviders() map[string]string {
	m := make(map[string]string, len(seededLLMProviders))
	for _, p := range seededLLMProviders {
		m[p.Name] = p.ProviderUrl
	}
	return m
}

// initLLMProviders applies seededLLMProviders to the store on every boot.
//
// State / IsDefault INVARIANT (admin-owned runtime toggles):
//
//	The seed sets State and IsDefault ONLY on initial CREATE. For an
//	already-existing record they are NEVER overwritten — an admin toggle made
//	via /v1/admin/providers persists across restarts. Type / SubType /
//	ProviderUrl / ClientSecret DO continue to self-heal from the seed on every
//	boot (so a stale upstream URL or KMS reference is corrected automatically);
//	State and IsDefault are deliberately excluded from that self-heal because
//	they are operator decisions, not canonical facts about the upstream.
//
// That self-heal is also why a bad seed cannot be fixed in the database: editing
// the row is reverted on the next boot, and deleting it is re-created. A row this
// table should not hold has to be dropped from the table AND pruned — which is
// what pruneStaleZenSeed does for zen.
func initLLMProviders() {
	for _, p := range seededLLMProviders {
		existing, err := getProvider("admin", p.Name)
		if err != nil {
			fmt.Printf("[init] WARNING: failed to check provider %q: %v\n", p.Name, err)
			continue
		}
		if existing != nil {
			// Ensure the provider type, model, endpoint, and secret reference
			// match the canonical definition. This lets us re-point a provider
			// (e.g. a stale upstream URL or an empty/expired secret) by editing
			// the seed table — the record self-heals on the next boot instead of
			// requiring a manual DB edit.
			//
			// NOTE: State and IsDefault are INTENTIONALLY NOT self-healed here —
			// they are admin-owned runtime toggles (see the invariant above). A
			// provider an operator disabled via /v1/admin/providers must stay
			// disabled across restarts; re-syncing State from the seed would
			// silently revert that decision on the next boot.
			needsUpdate := false
			if existing.Type != p.Type {
				fmt.Printf("[init] Fixing provider %q type: %q -> %q\n", p.Name, existing.Type, p.Type)
				existing.Type = p.Type
				needsUpdate = true
			}
			if existing.SubType != p.SubType {
				fmt.Printf("[init] Fixing provider %q model: %q -> %q\n", p.Name, existing.SubType, p.SubType)
				existing.SubType = p.SubType
				needsUpdate = true
			}
			if p.ProviderUrl != "" && existing.ProviderUrl != p.ProviderUrl {
				fmt.Printf("[init] Fixing provider %q url: %q -> %q\n", p.Name, existing.ProviderUrl, p.ProviderUrl)
				existing.ProviderUrl = p.ProviderUrl
				needsUpdate = true
			}
			// Re-sync the secret only when the canonical value is a KMS reference
			// (resolved at call time) and the stored value differs. Never clobber
			// a real stored secret with an empty seed value.
			if p.ClientSecret != "" && existing.ClientSecret != p.ClientSecret {
				fmt.Printf("[init] Fixing provider %q secret reference -> %q\n", p.Name, p.ClientSecret)
				existing.ClientSecret = p.ClientSecret
				needsUpdate = true
			}
			if needsUpdate {
				_, err = UpdateProvider("admin/"+p.Name, existing)
				if err != nil {
					fmt.Printf("[init] WARNING: failed to update provider %q: %v\n", p.Name, err)
				}
			}
			continue
		}
		// Initial CREATE: the seed's State and IsDefault become the starting
		// value. From here on they are owned by the admin (never re-clobbered).
		p.CreatedTime = util.GetCurrentTime()
		_, err = AddProvider(&p)
		if err != nil && !isDuplicateKeyErr(err) {
			fmt.Printf("[init] WARNING: failed to create provider %q: %v\n", p.Name, err)
		} else {
			fmt.Printf("[init] Created LLM provider: %s (%s)\n", p.Name, p.DisplayName)
		}
	}
	pruneStaleZenSeed()
}

// pruneStaleZenSeed deletes the `zen` provider row an earlier seed created.
//
// A model FAMILY's address is deployment config; familyProvider treats an admin
// row of the family's name as an operator OVERRIDE that wins over it. So seeding
// one hijacks the family — which is what happened. `zen` was seeded at do-ai's
// base "https://inference.do-ai.run/v1", and the family paths append their own
// /v1 (discovery does base+"/v1/models"), so every catalog refresh hit
// .../v1/v1/models, 404'd, and NO zen model was listed or served. Sibling `enso`
// — same serving binary, same family shape — was never seeded and never broke.
// That A/B is the entire diagnosis.
//
// The openrouter seed above carries the same lesson and was fixed by dropping the
// /v1. zen needs the row GONE rather than repointed: no route targets it (see
// modelCountByProvider), so it holds nothing, and merely dropping the /v1 would
// aim the family at DigitalOcean's catalog instead of the zen service that
// actually serves zen SKUs.
//
// Removing the seed entry is necessary but NOT sufficient: the loop above only
// visits names it seeds, so an already-created row would survive every restart
// and keep hijacking the family. Scoped to the exact stale shape, so a row an
// operator writes on purpose is never touched.
func pruneStaleZenSeed() {
	row, err := getProvider("admin", "zen")
	if err != nil {
		fmt.Printf("[init] WARNING: failed to check provider %q: %v\n", "zen", err)
		return
	}
	if row == nil || row.Type != "DigitalOcean" || row.ProviderUrl != "https://inference.do-ai.run/v1" {
		return // absent, or an operator's own override — leave it alone
	}
	if _, err := DeleteProvider(row); err != nil {
		fmt.Printf("[init] WARNING: failed to delete stale %q provider row: %v\n", "zen", err)
		return
	}
	fmt.Printf("[init] Removed stale %q family provider row — zen now resolves from ZEN_URL\n", "zen")
}

// isDuplicateKeyErr reports whether err is a unique-constraint violation
// from any supported database (MySQL, PostgreSQL, SQLite). All three drivers
// surface different error strings, but we only ever care that the row already
// exists — initBuiltInProviders re-runs on every restart and is intentionally
// idempotent.
func isDuplicateKeyErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "Duplicate entry"): // MySQL
		return true
	case strings.Contains(s, "duplicate key value violates unique"): // PostgreSQL
		return true
	case strings.Contains(s, "UNIQUE constraint failed"): // SQLite
		return true
	}
	return false
}
