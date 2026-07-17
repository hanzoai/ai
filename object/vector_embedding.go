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
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/hanzoai/ai/embedding"
	"github.com/hanzoai/ai/i18n"
	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/model"
	"github.com/hanzoai/ai/storage"
	"github.com/hanzoai/ai/txt"
)

// DEPRECATED — SQL `vector` table write path.
//
// Store ingestion NO LONGER writes the SQL `vector` table. The crossed wire is
// fixed: uploads/refreshes now ingest through object.IndexDocuments (Hanzo
// Vector + Hanzo Search — see store_ingest.go), the SAME unified index that
// /v1/chat retrieval (SearchDocuments) reads. The legacy SQL-write helpers
// (addEmbeddedVector / addVectorsForFile / addVectorsForStore) were removed;
// the `vector` table, its CRUD (vector.go), routes, and the legacy in-memory
// cosine read path (GetNearestKnowledge / search_default.go) remain ONLY for
// backward data access and the legacy message_answer flow, pending migration.
// filterTextFiles and withFileStatus below are reused by the new path.

func filterTextFiles(files []*storage.Object) []*storage.Object {
	fileTypes := txt.GetSupportedFileTypes()
	fileTypeMap := map[string]bool{}
	for _, fileType := range fileTypes {
		fileTypeMap[fileType] = true
	}
	res := []*storage.Object{}
	for _, file := range files {
		ext := filepath.Ext(file.Key)
		if fileTypeMap[ext] {
			res = append(res, file)
		}
	}
	return res
}

func withFileStatus(owner string, storeName string, fileKey string, op func() (bool, int, error)) (bool, error) {
	err := updateFileStatus(owner, storeName, fileKey, FileStatusProcessing, "", 0)
	if err != nil {
		log.Error("Failed to update file status for store: [%s], file: [%s]: %v", storeName, fileKey, err)
		return false, err
	}
	affected, tokenCount, opErr := op()
	fileStatus := FileStatusFinished
	errorText := ""
	if opErr != nil {
		fileStatus = FileStatusError
		errorText = opErr.Error()
	}
	err = updateFileStatus(owner, storeName, fileKey, fileStatus, errorText, tokenCount)
	if err != nil {
		log.Error("Failed to update file status for store: [%s], file: [%s]: %v", storeName, fileKey, err)
		return affected, errors.Join(opErr, err)
	}
	return affected, opErr
}

func getRelatedVectors(relatedStores []string, provider string) ([]*Vector, error) {
	vectors, err := getVectorsByProvider(relatedStores, provider)
	if err != nil {
		return nil, err
	}
	if len(vectors) == 0 {
		return nil, fmt.Errorf("no knowledge vectors found")
	}
	return vectors, nil
}

func queryVectorWithContext(embeddingProvider embedding.EmbeddingProvider, text string, timeout int, lang string) ([]float32, *embedding.EmbeddingResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(30+timeout*2)*time.Second)
	defer cancel()
	vector, embeddingResult, err := embeddingProvider.QueryVector(text, ctx, lang)
	return vector, embeddingResult, err
}

func queryVectorSafe(embeddingProvider embedding.EmbeddingProvider, text string, lang string) ([]float32, *embedding.EmbeddingResult, error) {
	var res []float32
	var embeddingResult *embedding.EmbeddingResult
	var err error
	for i := 0; i < 10; i++ {
		res, embeddingResult, err = queryVectorWithContext(embeddingProvider, text, i, lang)
		if err != nil {
			err = fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:queryVectorSafe() error, %s"), err.Error()))
			if i > 0 {
				log.Error("\tFailed (%d): %s", i+1, err.Error())
			}
		} else {
			break
		}
	}
	if err != nil {
		return nil, nil, err
	} else {
		return res, embeddingResult, nil
	}
}

func GetNearestKnowledge(storeName string, vectorStores []string, searchProviderType string, embeddingProvider *Provider, embeddingProviderObj embedding.EmbeddingProvider, modelProvider *Provider, owner string, text string, knowledgeCount int, lang string) ([]*model.RawMessage, []VectorScore, *embedding.EmbeddingResult, error) {
	searchProvider, err := GetSearchProvider(searchProviderType, owner)
	if err != nil {
		return nil, nil, nil, err
	}
	relatedStores := append(vectorStores, storeName)
	vectors, embeddingResult, err := searchProvider.Search(relatedStores, embeddingProvider.Name, embeddingProviderObj, modelProvider.Name, text, knowledgeCount, lang)
	if err != nil {
		if err.Error() == "no knowledge vectors found" {
			return nil, nil, embeddingResult, err
		} else {
			return nil, nil, nil, err
		}
	}
	vectorScores := []VectorScore{}
	knowledge := []*model.RawMessage{}
	for _, vector := range vectors {
		// if embeddingProvider.Name != vector.Provider {
		//	return "", nil, fmt.Errorf(i18n.Translate(lang, "object:The store's embedding provider: [%s] should equal to vector's embedding provider: [%s], vector = %v"), embeddingProvider.Name, vector.Provider, vector)
		// }
		vectorScores = append(vectorScores, VectorScore{
			Vector: vector.Name,
			Score:  vector.Score,
		})
		knowledge = append(knowledge, &model.RawMessage{
			Text:           vector.Text,
			Author:         "System",
			TextTokenCount: vector.TokenCount,
		})
	}
	return knowledge, vectorScores, embeddingResult, nil
}
