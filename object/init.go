// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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

package object

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hanzoai/cloud/conf"
	"github.com/hanzoai/cloud/util"
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
		if err != nil && !strings.Contains(err.Error(), "Duplicate entry") {
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
		if err != nil && !strings.Contains(err.Error(), "Duplicate entry") {
			panic(err)
		}
	}

	if embeddingProvider == nil {
		embeddingProvider = &Provider{
			Owner:       "admin",
			Name:        "dummy-embedding-provider",
			CreatedTime: util.GetCurrentTime(),
			DisplayName: "Dummy Embedding Provider",
			Category:    "Embedding",
			Type:        "Dummy",
			SubType:     "Dummy",
			IsDefault:   true,
		}
		_, err = AddProvider(embeddingProvider)
		if err != nil && !strings.Contains(err.Error(), "Duplicate entry") {
			panic(err)
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
			ClientSecret: conf.GetConfigString("doAiApiKey"),
			State:        "Active",
		},
		{
			Owner:        "admin",
			Name:         "fireworks",
			DisplayName:  "Fireworks AI",
			Category:     "Model",
			Type:         "Fireworks",
			SubType:      "qwen3-235b-a22b",
			ProviderUrl:  "https://api.fireworks.ai/inference/v1",
			ClientSecret: "kms://FIREWORKS_API_KEY",
			State:        "Active",
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
			State:        "Active",
		},
		{
			Owner:        "admin",
			Name:         "zen",
			DisplayName:  "Zen LM Gateway",
			Category:     "Model",
			Type:         "DigitalOcean",
			SubType:      "zen4",
			ProviderUrl:  "http://zen-gateway.zen.svc.cluster.local:4100/v1",
			ClientSecret: "kms://ZEN_GATEWAY_KEY",
			State:        "Active",
		},
	}

	for _, p := range providers {
		existing, err := getProvider("admin", p.Name)
		if err != nil {
			fmt.Printf("[init] WARNING: failed to check provider %q: %v\n", p.Name, err)
			continue
		}
		if existing != nil {
			// Ensure the provider type matches the canonical definition.
			// This corrects providers that were originally seeded with wrong types.
			if existing.Type != p.Type {
				fmt.Printf("[init] Fixing provider %q type: %q -> %q\n", p.Name, existing.Type, p.Type)
				existing.Type = p.Type
				_, err = UpdateProvider("admin/"+p.Name, existing)
				if err != nil {
					fmt.Printf("[init] WARNING: failed to update provider %q type: %v\n", p.Name, err)
				}
			}
			continue
		}

		p.CreatedTime = util.GetCurrentTime()
		_, err = AddProvider(&p)
		if err != nil && !strings.Contains(err.Error(), "Duplicate entry") {
			fmt.Printf("[init] WARNING: failed to create provider %q: %v\n", p.Name, err)
		} else {
			fmt.Printf("[init] Created LLM provider: %s (%s)\n", p.Name, p.DisplayName)
		}
	}
}
