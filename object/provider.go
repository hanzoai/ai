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
	"strings"

	"github.com/hanzoai/ai/agent"
	"github.com/hanzoai/ai/embedding"
	"github.com/hanzoai/ai/i18n"
	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/model"
	"github.com/hanzoai/ai/scan"
	"github.com/hanzoai/ai/storage"
	"github.com/hanzoai/ai/stt"
	"github.com/hanzoai/ai/tts"
	"github.com/hanzoai/ai/util"
	"github.com/hanzoai/dbx"
)

type Provider struct {
	Owner                        string             `db:"pk" json:"owner"`
	Name                         string             `db:"pk" json:"name"`
	CreatedTime                  string             `json:"createdTime"`
	DisplayName                  string             `json:"displayName"`
	Category                     string             `json:"category"`
	Type                         string             `json:"type"`
	SubType                      string             `json:"subType"`
	Flavor                       string             `json:"flavor"`
	ClientId                     string             `json:"clientId"`
	ClientSecret                 string             `json:"clientSecret"`
	Region                       string             `json:"region"`
	ProviderKey                  string             `json:"providerKey"`
	ProviderUrl                  string             `json:"providerUrl"`
	ApiVersion                   string             `json:"apiVersion"`
	CompatibleProvider           string             `json:"compatibleProvider"`
	McpTools                     agent.McpToolsList `json:"mcpTools"`
	Text                         string             `json:"text"`
	ConfigText                   string             `json:"configText"`
	RawText                      string             `json:"rawText"` // Raw result from scan (for Scan category providers)
	EnableThinking               bool               `json:"enableThinking"`
	Temperature                  float32            `json:"temperature"`
	TopP                         float32            `json:"topP"`
	TopK                         int                `json:"topK"`
	FrequencyPenalty             float32            `json:"frequencyPenalty"`
	PresencePenalty              float32            `json:"presencePenalty"`
	InputPricePerThousandTokens  float64            `json:"inputPricePerThousandTokens"`
	OutputPricePerThousandTokens float64            `json:"outputPricePerThousandTokens"`
	Currency                     string             `json:"currency"`
	UserKey                      string             `json:"userKey"`
	UserCert                     string             `json:"userCert"`
	SignKey                      string             `json:"signKey"`
	SignCert                     string             `json:"signCert"`
	ContractName                 string             `json:"contractName"`
	ContractMethod               string             `json:"contractMethod"`
	Network                      string             `json:"network"`
	Chain                        string             `json:"chain"`
	TestContent                  string             `json:"testContent"`
	// New fields for unified scan widget (for Scan category providers)
	TargetMode    string `json:"targetMode"`    // "Manual Input" or "Asset"
	Target        string `json:"target"`        // Manual input target (IP address or network range)
	Asset         string `json:"asset"`         // Selected asset for scan
	Runner        string `json:"runner"`        // Hostname about who runs the scan job
	ErrorText     string `json:"errorText"`     // Error message for the job execution
	ResultSummary string `json:"resultSummary"` // Short summary of scan results
	IsDefault     bool   `json:"isDefault"`
	IsRemote      bool   `json:"isRemote"`
	State         string `json:"state"`
	BrowserUrl    string `json:"browserUrl"`
}

// SecretMask is what the admin API returns in place of a stored secret, and
// therefore what the console posts back when an operator saves a form without
// touching the key field. Every site that hides a secret or recognises the
// placeholder on the way back in uses THIS — a masked value that one site writes
// and another fails to recognise is a value written into the store as if it were
// a key.
const SecretMask = "***"

// GetMaskedProvider returns the row with its credentials replaced by SecretMask,
// keeping only what the caller is entitled to read.
//
// There is no "skip the masking" argument. It had one, every call site passed
// true, and the false branch returned the row with every key in the clear — a
// default that only had to be reached once to spend real money. A caller that
// genuinely needs a live credential reads the row it already holds.
func GetMaskedProvider(provider *Provider, user *iam.User) *Provider {
	if provider == nil {
		return nil
	}
	if provider.ClientSecret != "" {
		provider.ClientSecret = SecretMask
	}
	// These four fields are PLATFORM upstream credentials — the money that buys a
	// call from OpenAI, Anthropic, OpenRouter, Fireworks. Whoever can read one can
	// spend it directly, off our meter, so the predicate that unmasks them is the
	// platform one: membership in the reserved admin org.
	//
	// IsAdmin is a TENANT fact — every customer who administers their own org
	// satisfies it — and a global provider row belongs to no tenant. IsSuperAdmin is
	// the narrower predicate IsAdmin's own doc comment names for exactly this
	// operation ("provider/upstream-key config"), and it is the one predicate the
	// whole estate agrees on: owner == AdminOrg.
	if !util.IsSuperAdmin(user) {
		if provider.ProviderKey != "" {
			provider.ProviderKey = SecretMask
		}
		if provider.UserKey != "" {
			provider.UserKey = SecretMask
		}
		if provider.ConfigText != "" {
			provider.ConfigText = SecretMask
		}
		if provider.SignKey != "" {
			provider.SignKey = SecretMask
		}
	}
	return provider
}

func GetMaskedProviders(providers []*Provider, user *iam.User) []*Provider {
	for _, provider := range providers {
		GetMaskedProvider(provider, user)
	}
	return providers
}

func GetGlobalProviders() ([]*Provider, error) {
	providers := []*Provider{}
	err := findAll(adapter.db, "provider", &providers, nil, "owner ASC", "created_time DESC")
	if err != nil {
		return providers, err
	}
	if providerAdapter != nil {
		providers2 := []*Provider{}
		err = findAll(providerAdapter.db, "provider", &providers2, nil, "owner ASC", "created_time DESC")
		if err != nil {
			return providers2, err
		}
		// Mark remote providers
		for _, provider := range providers2 {
			provider.IsRemote = true
		}
		providers = append(providers, providers2...)
	}
	return providers, nil
}

func GetProviders(owner string) ([]*Provider, error) {
	providers := []*Provider{}
	err := findAll(adapter.db, "provider", &providers, dbx.HashExp{"owner": owner}, "created_time DESC")
	if err != nil {
		return providers, err
	}
	if providerAdapter != nil {
		providers2 := []*Provider{}
		err = findAll(providerAdapter.db, "provider", &providers2, dbx.HashExp{"owner": owner}, "created_time DESC")
		if err != nil {
			return providers2, err
		}
		// Mark remote providers
		for _, provider := range providers2 {
			provider.IsRemote = true
		}
		providers = append(providers, providers2...)
	}
	return providers, nil
}

func getProvider(owner string, name string) (*Provider, error) {
	// No store is "I cannot answer", not a crash. This reads adapter.db, which is
	// nil before the DB is initialised — during boot, in the standalone runtime
	// with no driverName, and in every unit test — so an unguarded read turns a
	// missing dependency into a SIGSEGV at whatever call site happened to ask
	// first. Callers already handle (nil, err).
	if adapter == nil || adapter.db == nil {
		return nil, fmt.Errorf("provider store is not initialised")
	}
	provider := Provider{Owner: owner, Name: name}
	existed, err := getOne(adapter.db, "provider", &provider, pk2(provider.Owner, provider.Name))
	if err != nil {
		return &provider, err
	}
	if providerAdapter != nil && !existed {
		existed, err = getOne(providerAdapter.db, "provider", &provider, pk2(provider.Owner, provider.Name))
		if err != nil {
			return &provider, err
		}
		if existed {
			provider.IsRemote = true
		}
	}
	if existed {
		return &provider, nil
	} else {
		return nil, nil
	}
}

func GetProvider(id string) (*Provider, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return nil, err
	}
	return getProvider(owner, name)
}

func UpdateProvider(id string, provider *Provider) (bool, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return false, err
	}
	providerDb, err := getProvider(owner, name)
	if err != nil {
		return false, err
	}
	if provider == nil {
		return false, nil
	}
	provider.processProviderParams(providerDb)
	if providerAdapter != nil && provider.IsRemote {
		provider.Owner = owner
		provider.Name = name
		err = providerAdapter.db.Model(provider).Update()
		if err != nil {
			return false, err
		}
		// return affected != 0
		return true, nil
	}
	provider.Owner = owner
	provider.Name = name
	err = adapter.db.Model(provider).Update()
	if err != nil {
		return false, err
	}
	// return affected != 0
	return true, nil
}

func AddProvider(provider *Provider) (bool, error) {
	if provider.ProviderKey == "" && provider.Category == "Model" {
		provider.ProviderKey = generateProviderKey()
	}
	if providerAdapter != nil && provider.IsRemote {
		err := insertRow(providerAdapter.db, provider)
		if err != nil {
			return false, err
		}
		return true, nil
	}
	err := insertRow(adapter.db, provider)
	if err != nil {
		return false, err
	}
	return true, nil
}

func DeleteProvider(provider *Provider) (bool, error) {
	if providerAdapter != nil && provider.IsRemote {
		affected, err := deleteByPK(providerAdapter.db, "provider", pk2(provider.Owner, provider.Name))
		if err != nil {
			return false, err
		}
		return affected != 0, nil
	}
	affected, err := deleteByPK(adapter.db, "provider", pk2(provider.Owner, provider.Name))
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func (provider *Provider) GetId() string {
	return fmt.Sprintf("%s/%s", provider.Owner, provider.Name)
}

func GetDefaultKubernetesProvider(lang string) (*Provider, error) {
	providers, err := GetProviders("admin")
	if err != nil {
		return nil, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:failed to get providers: %v"), err))
	}
	for _, provider := range providers {
		if provider.Category == "Private Cloud" && provider.Type == "Kubernetes" && provider.State == "Active" {
			return provider, nil
		}
	}
	return nil, fmt.Errorf("%s", i18n.Translate(lang, "object:no Kubernetes provider found"))
}

func (p *Provider) GetStorageProviderObj(vectorStoreId string, lang string) (storage.StorageProvider, error) {
	pProvider, err := storage.GetStorageProvider(p.Type, p.ClientId, p.ClientSecret, p.Name, vectorStoreId, lang)
	if err != nil {
		return nil, err
	}
	if pProvider == nil {
		return nil, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:the storage provider type: %s is not supported"), p.Type))
	}
	return pProvider, nil
}

func (p *Provider) GetModelProvider(lang string) (model.ModelProvider, error) {
	pProvider, err := model.GetModelProvider(p.Type, p.SubType, p.ClientId, p.ClientSecret, p.UserKey, p.Temperature, p.TopP, p.TopK, p.FrequencyPenalty, p.PresencePenalty, p.ProviderUrl, p.ApiVersion, p.CompatibleProvider, p.InputPricePerThousandTokens, p.OutputPricePerThousandTokens, p.Currency, p.EnableThinking)
	if err != nil {
		return nil, err
	}
	if pProvider == nil {
		return nil, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:the model provider type: %s is not supported"), p.Type))
	}
	return pProvider, nil
}

func (p *Provider) GetEmbeddingProvider(lang string) (embedding.EmbeddingProvider, error) {
	pProvider, err := embedding.GetEmbeddingProvider(p.Type, p.SubType, p.ClientId, p.ClientSecret, p.ProviderUrl, p.ApiVersion, p.InputPricePerThousandTokens, p.Currency, lang)
	if err != nil {
		return nil, err
	}
	if pProvider == nil {
		return nil, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:the embedding provider type: %s is not supported"), p.Type))
	}
	return pProvider, nil
}

func (p *Provider) GetAgentProvider(lang string) (agent.AgentProvider, error) {
	pProvider, err := agent.GetAgentProvider(p.Type, p.SubType, p.Text, p.McpTools, lang)
	if err != nil {
		return nil, err
	}
	if pProvider == nil {
		return nil, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "agent:the agent provider type: %s is not supported"), p.Type))
	}
	return pProvider, nil
}

func (p *Provider) GetTextToSpeechProvider(lang string, format string) (tts.TextToSpeechProvider, error) {
	pProvider, err := tts.GetTextToSpeechProvider(p.Type, p.SubType, p.ClientId, p.ClientSecret, p.ProviderUrl, p.ApiVersion, p.InputPricePerThousandTokens, p.Currency, p.Flavor, format, lang)
	if err != nil {
		return nil, err
	}
	if pProvider == nil {
		return nil, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:the TTS provider type: %s is not supported"), p.Type))
	}
	return pProvider, nil
}

func (p *Provider) GetSpeechToTextProvider(lang string) (stt.SpeechToTextProvider, error) {
	pProvider, err := stt.GetSpeechToTextProvider(p.Type, p.SubType, p.ClientSecret, p.ProviderUrl, p.Flavor)
	if err != nil {
		return nil, err
	}
	if pProvider == nil {
		return nil, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:the STT provider type: %s is not supported"), p.Type))
	}
	return pProvider, nil
}

func (p *Provider) GetScanProvider(lang string) (scan.ScanProvider, error) {
	pProvider, err := scan.GetScanProvider(p.Type, p.ClientId, lang)
	if err != nil {
		return nil, err
	}
	if pProvider == nil {
		return nil, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:the scan provider type: %s is not supported"), p.Type))
	}
	return pProvider, nil
}

func GetModelProviderFromContext(owner string, name string, lang string) (*Provider, model.ModelProvider, error) {
	var providerName string
	if name != "" {
		providerName = name
	} else {
		store, err := GetDefaultStore(owner)
		if err != nil {
			return nil, nil, err
		}
		if store != nil && store.ModelProvider != "" {
			providerName = store.ModelProvider
		}
	}
	return getModelProviderFromName(owner, providerName, lang)
}

func GetEmbeddingProviderFromContext(owner string, name string, lang string) (*Provider, embedding.EmbeddingProvider, error) {
	var providerName string
	if name != "" {
		providerName = name
	} else {
		store, err := GetDefaultStore(owner)
		if err != nil {
			return nil, nil, err
		}
		if store != nil && store.EmbeddingProvider != "" {
			providerName = store.EmbeddingProvider
		}
	}
	return getEmbeddingProviderFromName(owner, providerName, lang)
}

func GetAgentProviderFromContext(owner string, name string, lang string) (*Provider, agent.AgentProvider, error) {
	var providerName string
	if name != "" {
		providerName = name
	} else {
		store, err := GetDefaultStore(owner)
		if err != nil {
			return nil, nil, err
		}
		if store != nil && store.AgentProvider != "" {
			providerName = store.AgentProvider
		}
	}
	return getAgentProviderFromName(owner, providerName, lang)
}

func GetAgentClients(agentProviderObj agent.AgentProvider) (*agent.AgentClients, error) {
	if agentProviderObj == nil {
		return nil, nil
	}
	return agentProviderObj.GetAgentClients()
}

func GetProviderCount(owner, storeName, field, value string) (int64, error) {
	session := GetDbQuery(owner, -1, -1, field, value, "", "")
	if storeName != "" {
		store, err := GetStore(util.GetIdFromOwnerAndName(owner, storeName))
		if err != nil {
			return 0, err
		}
		providerNames := collectProviderNames(store)
		if len(providerNames) > 0 {
			session = session.AndWhere(dbx.In("name", toInterfaceSlice(providerNames)...))
		}
	}
	count, err := queryCount(session, "provider")
	if err != nil {
		return 0, err
	}
	// Add count from remote adapter if available
	if providerAdapter != nil {
		session2, err := buildRemoteProviderQuery(owner, field, value, storeName)
		if err != nil {
			return count, err
		}
		count2, err := queryCount(session2, "provider")
		if err != nil {
			return count, err
		}
		count += count2
	}
	return count, nil
}

func collectProviderNames(store *Store) []string {
	var providerNames []string
	if store.StorageProvider != "" {
		providerNames = append(providerNames, store.StorageProvider)
	}
	if store.ImageProvider != "" {
		providerNames = append(providerNames, store.ImageProvider)
	}
	if store.SplitProvider != "" {
		providerNames = append(providerNames, store.SplitProvider)
	}
	if store.SearchProvider != "" {
		providerNames = append(providerNames, store.SearchProvider)
	}
	if store.ModelProvider != "" {
		providerNames = append(providerNames, store.ModelProvider)
	}
	if store.EmbeddingProvider != "" {
		providerNames = append(providerNames, store.EmbeddingProvider)
	}
	if store.TextToSpeechProvider != "" {
		providerNames = append(providerNames, store.TextToSpeechProvider)
	}
	if store.SpeechToTextProvider != "" {
		providerNames = append(providerNames, store.SpeechToTextProvider)
	}
	if store.AgentProvider != "" {
		providerNames = append(providerNames, store.AgentProvider)
	}
	if store.ChildModelProviders != nil {
		providerNames = append(providerNames, store.ChildModelProviders...)
	}
	return providerNames
}

func buildRemoteProviderQuery(owner, field, value, storeName string) (*dbx.SelectQuery, error) {
	if providerAdapter == nil {
		return nil, fmt.Errorf("providerAdapter is nil")
	}
	q := providerAdapter.db.Select().From("provider")
	if owner != "" {
		q = q.AndWhere(dbx.HashExp{"owner": owner})
	}
	if field != "" && value != "" {
		if util.FilterField(field) {
			q = q.AndWhere(dbx.Like(util.SnakeString(field), value))
		}
	}
	if storeName != "" {
		store, err := GetStore(util.GetIdFromOwnerAndName(owner, storeName))
		if err != nil {
			return nil, err
		}
		providerNames := collectProviderNames(store)
		if len(providerNames) > 0 {
			q = q.AndWhere(dbx.In("name", toInterfaceSlice(providerNames)...))
		}
	}
	return q, nil
}

func GetPaginationProviders(owner, storeName string, offset, limit int, field, value, sortField, sortOrder string) ([]*Provider, error) {
	providers := []*Provider{}
	// Fetch from local adapter without pagination to properly merge with remote providers
	session := GetDbQuery(owner, -1, -1, field, value, sortField, sortOrder)
	if storeName != "" {
		store, err := GetStore(util.GetIdFromOwnerAndName(owner, storeName))
		if err != nil {
			return providers, err
		}
		providerNames := collectProviderNames(store)
		if len(providerNames) > 0 {
			session = session.AndWhere(dbx.In("name", toInterfaceSlice(providerNames)...))
		}
	}
	err := queryFind(session, "provider", &providers)
	if err != nil {
		return providers, err
	}
	// Fetch from remote adapter if available
	if providerAdapter != nil {
		providers2 := []*Provider{}
		session2, err := buildRemoteProviderQuery(owner, field, value, storeName)
		if err != nil {
			return providers, err
		}
		// Same sort as the local half above, through the same whitelist. These
		// two result sets are concatenated, so they must agree on the column as
		// well as on the direction — and this half reads a DIFFERENT database,
		// which is what an unchecked ORDER BY here would let a caller walk.
		session2 = session2.OrderBy(sortColumn(sortField, sortOrder) + sortDirection(sortOrder))
		err = queryFind(session2, "provider", &providers2)
		if err != nil {
			return providers, err
		}
		// Mark remote providers
		for _, provider := range providers2 {
			provider.IsRemote = true
		}
		// Append remote providers after local providers
		providers = append(providers, providers2...)
	}
	// Apply pagination on merged results
	if offset != -1 && limit != -1 {
		start := offset
		end := offset + limit
		if start >= len(providers) {
			return []*Provider{}, nil
		}
		if end > len(providers) {
			end = len(providers)
		}
		providers = providers[start:end]
	}
	return providers, nil
}

func RefreshMcpTools(provider *Provider) error {
	tools, err := agent.GetToolsList(provider.Text)
	if err != nil {
		return err
	}
	provider.McpTools = tools
	return nil
}

func (p *Provider) processProviderParams(providerDb *Provider) {
	if p.ClientSecret == SecretMask {
		p.ClientSecret = providerDb.ClientSecret
	}
	if p.UserKey == SecretMask {
		p.UserKey = providerDb.UserKey
	}
	if p.SignKey == SecretMask {
		p.SignKey = providerDb.SignKey
	}
	if p.ProviderKey == "" && p.Category == "Model" {
		p.ProviderKey = generateProviderKey()
	}
	if p.Type == "Ollama" && p.ProviderUrl != "" && !strings.HasPrefix(p.ProviderUrl, "http") {
		p.ProviderUrl = "http://" + p.ProviderUrl
	}
	if p.Category == "Model" && p.Type == "OpenAI" && (strings.Contains(p.SubType, "o1") || strings.Contains(p.SubType, "o3") || strings.Contains(p.SubType, "o4")) {
		p.Temperature = 1
		p.TopP = 1
		p.FrequencyPenalty = 0
		p.PresencePenalty = 0
	}
}
