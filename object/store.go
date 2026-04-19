// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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
	"time"

	"github.com/hanzoai/cloud/i18n"
	"github.com/hanzoai/cloud/storage"
	"github.com/hanzoai/cloud/util"
	"github.com/hanzoai/dbx"
)

type TreeFile struct {
	Key         string               `json:"key"`
	Title       string               `json:"title"`
	Size        int64                `json:"size"`
	CreatedTime string               `json:"createdTime"`
	IsLeaf      bool                 `json:"isLeaf"`
	Url         string               `json:"url"`
	Children    []*TreeFile          `json:"children"`
	ChildrenMap map[string]*TreeFile `db:"-" json:"-"`
}
type Properties struct {
	CollectedTime string `json:"collectedTime"`
	Subject       string `json:"subject"`
}
type UsageInfo struct {
	Provider   string    `json:"provider"`
	TokenCount int       `json:"tokenCount"`
	StartTime  time.Time `json:"startTime"`
}
type ExampleQuestion struct {
	Title string `json:"title"`
	Text  string `json:"text"`
	Image string `json:"image"`
}
type Store struct {
	Owner                string                 `db:"pk" json:"owner"`
	Name                 string                 `db:"pk" json:"name"`
	CreatedTime          string                 `json:"createdTime"`
	DisplayName          string                 `json:"displayName"`
	StorageProvider      string                 `json:"storageProvider"`
	StorageSubpath       string                 `json:"storageSubpath"`
	ImageProvider        string                 `json:"imageProvider"`
	SplitProvider        string                 `json:"splitProvider"`
	SearchProvider       string                 `json:"searchProvider"`
	ModelProvider        string                 `json:"modelProvider"`
	EmbeddingProvider    string                 `json:"embeddingProvider"`
	TextToSpeechProvider string                 `json:"textToSpeechProvider"`
	EnableTtsStreaming   bool                   `json:"enableTtsStreaming"`
	SpeechToTextProvider string                 `json:"speechToTextProvider"`
	AgentProvider        string                 `json:"agentProvider"`
	VectorStoreId        string                 `json:"vectorStoreId"`
	BuiltinTools         StringSlice            `json:"builtinTools"`
	MemoryLimit          int                    `json:"memoryLimit"`
	Frequency            int                    `json:"frequency"`
	LimitMinutes         int                    `json:"limitMinutes"`
	KnowledgeCount       int                    `json:"knowledgeCount"`
	SuggestionCount      int                    `json:"suggestionCount"`
	Welcome              string                 `json:"welcome"`
	WelcomeTitle         string                 `json:"welcomeTitle"`
	WelcomeText          string                 `json:"welcomeText"`
	Prompt               string                 `json:"prompt"`
	ExampleQuestions     []ExampleQuestion      `json:"exampleQuestions"`
	ThemeColor           string                 `json:"themeColor"`
	Avatar               string                 `json:"avatar"`
	Title                string                 `json:"title"`
	HtmlTitle            string                 `json:"htmlTitle"`
	FaviconUrl           string                 `json:"faviconUrl"`
	LogoUrl              string                 `json:"logoUrl"`
	FooterHtml           string                 `json:"footerHtml"`
	NavItems             StringSlice            `json:"navItems"`
	VectorStores         StringSlice            `json:"vectorStores"`
	ChildStores          StringSlice            `json:"childStores"`
	ChildModelProviders  StringSlice            `json:"childModelProviders"`
	ForbiddenWords       StringSlice            `json:"forbiddenWords"`
	ShowAutoRead         bool                   `json:"showAutoRead"`
	DisableFileUpload    bool                   `json:"disableFileUpload"`
	HideThinking         bool                   `json:"hideThinking"`
	IsDefault            bool                   `json:"isDefault"`
	State                string                 `json:"state"`
	ChatCount            int                    `db:"-" json:"chatCount"`
	MessageCount         int                    `db:"-" json:"messageCount"`
	FileTree             *TreeFile              `json:"fileTree"`
	PropertiesMap        map[string]*Properties `json:"propertiesMap"`
}

func GetGlobalStores() ([]*Store, error) {
	stores := []*Store{}
	err := findAll(adapter.db, "store", &stores, nil, "owner ASC", "created_time DESC")
	if err != nil {
		return stores, err
	}
	return stores, nil
}

func GetStores(owner string) ([]*Store, error) {
	stores := []*Store{}
	err := findAll(adapter.db, "store", &stores, dbx.HashExp{"owner": owner}, "created_time DESC")
	if err != nil {
		return stores, err
	}
	return stores, nil
}

func GetDefaultStore(owner string) (*Store, error) {
	stores, err := GetStores(owner)
	if err != nil {
		return nil, err
	}
	for _, store := range stores {
		if store.IsDefault {
			return store, nil
		}
	}
	for _, store := range stores {
		if store.State != "Inactive" && store.StorageProvider != "" && store.ModelProvider != "" && store.EmbeddingProvider != "" {
			return store, nil
		}
	}
	if len(stores) > 0 {
		return stores[0], nil
	}
	return nil, nil
}

func getStore(owner string, name string) (*Store, error) {
	store := Store{Owner: owner, Name: name}
	existed, err := getOne(adapter.db, "store", &store, pk2(store.Owner, store.Name))
	if err != nil {
		return &store, err
	}
	if existed {
		return &store, nil
	}
	return nil, nil
}

func GetStore(id string) (*Store, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return nil, err
	}
	return getStore(owner, name)
}

func UpdateStore(id string, store *Store) (bool, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return false, err
	}
	_, err = getStore(owner, name)
	if err != nil {
		return false, err
	}
	if store == nil {
		return false, nil
	}
	store.Owner = owner
	store.Name = name
	err = adapter.db.Model(store).Update()
	if err != nil {
		return false, err
	}
	// return affected != 0
	return true, nil
}

func AddStore(store *Store) (bool, error) {
	err := insertRow(adapter.db, store)
	affected := int64(1)
	if err != nil {
		affected = 0
	}
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func DeleteStore(store *Store) (bool, error) {
	affected, err := deleteByPK(adapter.db, "store", pk2(store.Owner, store.Name))
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func (store *Store) GetId() string {
	return fmt.Sprintf("%s/%s", store.Owner, store.Name)
}

func (store *Store) GetStorageProviderObj(lang string) (storage.StorageProvider, error) {
	var provider *Provider
	var err error
	if store.StorageProvider == "" {
		provider, err = GetDefaultStorageProvider()
	} else {
		providerId := util.GetIdFromOwnerAndName(store.Owner, store.StorageProvider)
		provider, err = GetProvider(providerId)
	}
	if err != nil {
		return nil, err
	}
	var storageProvider storage.StorageProvider
	if provider != nil {
		storageProvider, err = provider.GetStorageProviderObj(store.VectorStoreId, lang)
		if err != nil {
			return nil, err
		}
	} else {
		storageProvider, err = storage.NewIamProvider(store.StorageProvider, lang)
		if err != nil {
			return nil, err
		}
	}
	return NewSubpathStorageProvider(storageProvider, store.StorageSubpath), nil
}

func (store *Store) GetImageProviderObj(lang string) (storage.StorageProvider, error) {
	if store.ImageProvider == "" {
		return nil, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:The image provider for store: %s should not be empty"), store.GetId()))
	}
	return storage.NewIamProvider(store.ImageProvider, lang)
}

func (store *Store) GetModelProvider() (*Provider, error) {
	if store.ModelProvider == "" {
		return GetDefaultModelProvider()
	}
	providerId := util.GetIdFromOwnerAndName(store.Owner, store.ModelProvider)
	return GetProvider(providerId)
}

func (store *Store) GetTextToSpeechProvider() (*Provider, error) {
	if store.TextToSpeechProvider == "" {
		return GetDefaultTextToSpeechProvider()
	}
	providerId := util.GetIdFromOwnerAndName(store.Owner, store.TextToSpeechProvider)
	return GetProvider(providerId)
}

func (store *Store) GetSpeechToTextProvider() (*Provider, error) {
	if store.SpeechToTextProvider == "" {
		return GetDefaultSpeechToTextProvider()
	}
	providerId := util.GetIdFromOwnerAndName(store.Owner, store.SpeechToTextProvider)
	return GetProvider(providerId)
}

func (store *Store) GetEmbeddingProvider() (*Provider, error) {
	if store.EmbeddingProvider == "" {
		return GetDefaultEmbeddingProvider()
	}
	providerId := util.GetIdFromOwnerAndName(store.Owner, store.EmbeddingProvider)
	return GetProvider(providerId)
}

func RefreshStoreVectors(store *Store, lang string) (bool, error) {
	storageProviderObj, err := store.GetStorageProviderObj(lang)
	if err != nil {
		return false, err
	}
	modelProvider, err := store.GetModelProvider()
	if err != nil {
		return false, err
	}
	if modelProvider == nil {
		return false, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:The model provider for store: %s is not found"), store.GetId()))
	}
	embeddingProvider, err := store.GetEmbeddingProvider()
	if err != nil {
		return false, err
	}
	if embeddingProvider == nil {
		return false, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:The embedding provider for store: %s is not found"), store.GetId()))
	}
	embeddingProviderObj, err := embeddingProvider.GetEmbeddingProvider(lang)
	if err != nil {
		return false, err
	}
	err = UpdateFilesStatusByStore(store.Owner, store.Name, FileStatusPending)
	if err != nil {
		return false, err
	}
	_, err = DeleteVectorsByStore(store.Owner, store.Name)
	if err != nil {
		return false, err
	}
	ok, err := addVectorsForStore(storageProviderObj, embeddingProviderObj, "", store.Owner, store.Name, store.SplitProvider, embeddingProvider.Name, modelProvider.SubType, lang)
	return ok, err
}

func AddVectorsForFile(store *Store, fileName string, fileUrl string, lang string) (bool, error) {
	modelProvider, err := store.GetModelProvider()
	if err != nil {
		return false, err
	}
	if modelProvider == nil {
		return false, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:The model provider for store: %s is not found"), store.GetId()))
	}
	embeddingProvider, err := store.GetEmbeddingProvider()
	if err != nil {
		return false, err
	}
	if embeddingProvider == nil {
		return false, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:The embedding provider for store: %s is not found"), store.GetId()))
	}
	embeddingProviderObj, err := embeddingProvider.GetEmbeddingProvider(lang)
	if err != nil {
		return false, err
	}
	ok, err := withFileStatus(store.Owner, store.Name, fileName, func() (bool, int, error) {
		return addVectorsForFile(embeddingProviderObj, store.Name, fileName, fileUrl, store.SplitProvider, embeddingProvider.Name, modelProvider.SubType, lang)
	})
	return ok, err
}

func RefreshFileVectors(file *File, lang string) (bool, error) {
	store, err := getStore(file.Owner, file.Store)
	if err != nil {
		return false, err
	}
	if store == nil {
		return false, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "account:The store: %s is not found"), file.Store))
	}
	var objectKey string
	prefix := fmt.Sprintf("%s_", file.Store)
	if strings.HasPrefix(file.Name, prefix) {
		objectKey = strings.TrimPrefix(file.Name, prefix)
	} else {
		objectKey = file.Name
	}
	if objectKey == "" {
		return false, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:The file: %s is not found"), file.Name))
	}
	if file.Url == "" {
		return false, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:The file URL for: %s is empty"), file.Name))
	}
	_, err = DeleteVectorsByFile(store.Owner, store.Name, objectKey)
	if err != nil {
		return false, err
	}
	return AddVectorsForFile(store, objectKey, file.Url, lang)
}

func refreshVector(vector *Vector, lang string) (bool, error) {
	_, embeddingProviderObj, err := getEmbeddingProviderFromName("admin", vector.Provider, lang)
	if err != nil {
		return false, err
	}
	data, _, err := queryVectorSafe(embeddingProviderObj, vector.Text, lang)
	if err != nil {
		return false, err
	}
	vector.Data = data
	return true, nil
}

func GetStoresByFields(owner string, fields ...string) ([]*Store, error) {
	var stores []*Store
	err := adapter.db.Select(fields...).From("store").Where(dbx.HashExp{"owner": owner}).OrderBy("created_time DESC").All(&stores)
	if err != nil {
		return nil, err
	}
	return stores, nil
}

func GetStoreCount(name, field, value string) (int64, error) {
	q := GetDbQuery("", -1, -1, field, value, "", "")
	return queryCount(q, "store")
}

func GetPaginationStores(offset, limit int, name, field, value, sortField, sortOrder string) ([]*Store, error) {
	stores := []*Store{}
	q := GetDbQuery("", offset, limit, field, value, sortField, sortOrder)
	if name != "" {
		q = q.AndWhere(dbx.HashExp{"name": name})
	}
	err := queryFind(q, "store", &stores)
	if err != nil {
		return stores, err
	}
	return stores, nil
}

func (store *Store) ContainsForbiddenWords(text string) (bool, string) {
	if store.ForbiddenWords == nil || len(store.ForbiddenWords) == 0 {
		return false, ""
	}
	lowerText := strings.ToLower(text)
	for _, forbiddenWord := range store.ForbiddenWords {
		if forbiddenWord == "" {
			continue
		}
		lowerForbiddenWord := strings.ToLower(forbiddenWord)
		if strings.Contains(lowerText, lowerForbiddenWord) {
			return true, forbiddenWord
		}
	}
	return false, ""
}
