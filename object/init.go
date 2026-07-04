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
	// silently broke ingest. Type "Local" is the OpenAI-compatible client that HONORS
	// ProviderUrl (unlike "OpenAI", which hardcodes api.openai.com), so it calls
	// DigitalOcean GenAI's OpenAI-shaped /v1/embeddings — the SAME inference.do-ai.run
	// endpoint + DO_AI_API_KEY the working do-ai model provider uses. SubType is DO's
	// served embedding model.
	if embeddingProvider == nil {
		embeddingProvider = &Provider{
			Owner:        "admin",
			Name:         "do-embed",
			CreatedTime:  util.GetCurrentTime(),
			DisplayName:  "DigitalOcean Embeddings",
			Category:     "Embedding",
			Type:         "Local",
			SubType:      "qwen3-embedding-0.6b",
			ProviderUrl:  "https://inference.do-ai.run/v1",
			ClientSecret: "kms://DO_AI_API_KEY",
			State:        "Active",
			IsDefault:    true,
		}
		_, err = AddProvider(embeddingProvider)
		if err != nil && !isDuplicateKeyErr(err) {
			panic(err)
		}
	} else if embeddingProvider.Type == "Dummy" {
		// Upgrade the legacy Dummy default IN PLACE (keep its Name + IsDefault) so
		// existing deployments self-heal to real embeddings on the next boot.
		embeddingProvider.DisplayName = "DigitalOcean Embeddings"
		embeddingProvider.Type = "Local"
		embeddingProvider.SubType = "qwen3-embedding-0.6b"
		embeddingProvider.ProviderUrl = "https://inference.do-ai.run/v1"
		embeddingProvider.ClientSecret = "kms://DO_AI_API_KEY"
		embeddingProvider.State = "Active"
		if _, uerr := UpdateProvider("admin/"+embeddingProvider.Name, embeddingProvider); uerr != nil {
			fmt.Printf("[init] WARNING: failed to upgrade embedding provider %q to real DO embeddings: %v\n", embeddingProvider.Name, uerr)
		}
	}
	ttsProviderName := "Browser Built-In"
	if ttsProvider != nil {
		ttsProviderName = ttsProvider.Name
	}
	sttProviderName := "Browser Built-In"
	return modelProvider.Name, embeddingProvider.Name, ttsProviderName, sttProviderName
}

// initLLMProviders bootstraps the LLM provider records needed by the
// model routing table (see controllers/model_routes.go). Each provider
// maps to an upstream service with its own API key and base URL.
//
// Provider secrets can use KMS references ("kms://SECRET_NAME") which
// are resolved at runtime via ResolveProviderSecret().
//
// DO-first defaults: do-ai is the primary (State Active, IsDefault true) —
// the universal DigitalOcean GenAI router that backs OpenAI/Anthropic/Llama/
// DeepSeek/Qwen/GLM/Kimi. fireworks and openai-direct ship DISABLED (an admin
// opts in via /v1/admin/providers/toggle). zen stays Active: it is the branded
// first-party family served over do-ai (same key, no GPU) and must keep working.
// openrouter ships DISABLED (toggleable), keeping "DO-first" the default while
// making the catalog manageable in the same table.
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
func initLLMProviders() {
	providers := []Provider{
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
			Owner:        "admin",
			Name:         "zen",
			DisplayName:  "Zen LM (Hanzo)",
			Category:     "Model",
			Type:         "DigitalOcean",
			SubType:      "glm-5",
			ProviderUrl:  "https://inference.do-ai.run/v1",
			ClientSecret: "kms://DO_AI_API_KEY",
			State:        "Active", // branded first-party family over do-ai — keep on
			IsDefault:    false,
		},
		{
			Owner:        "admin",
			Name:         "openrouter",
			DisplayName:  "OpenRouter",
			Category:     "Model",
			Type:         "OpenRouter",
			SubType:      "openrouter/auto",
			ProviderUrl:  "https://openrouter.ai/api/v1",
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
			// videoUpstreamBase yields the clean /v1 base. Active so the video
			// routes work; the branded backend model name is never exposed.
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
	for _, p := range providers {
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
