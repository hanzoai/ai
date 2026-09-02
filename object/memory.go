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

// Memory is the cloud-resident backend of the unified memory interface. It is a
// peer to hanzo-mcp's on-disk local backend: same action surface (remember,
// search, list, recall, update, delete, facts), different storage. Memories are
// persisted in the same store (dbx/SQLite/Postgres) the rest of the AI core
// uses and reuse the existing embedding provider for semantic search — no new
// provider, no new secret, no standalone service.
//
// SECURITY INVARIANT: every accessor is scoped by BOTH owner (org) and userId.
// The owner/userId are supplied by the controller from the gateway-injected IAM
// identity, never from the request body. A memory's primary key is (owner,
// name); userId is a separate column, so single-row lookups additionally verify
// userId to stop a caller in the same org from reading another user's memory by
// guessing its name.
package object

import (
	"database/sql/driver"
	"fmt"
	"strings"

	"github.com/hanzoai/ai/util"
	"github.com/hanzoai/dbx"
)

// Memory kinds mirror the hanzo-mcp memory tool's taxonomy so the local and
// cloud backends present one interface.
const (
	MemoryKindUser      = "user"      // facts/preferences about the user
	MemoryKindFeedback  = "feedback"  // corrections the user gave the assistant
	MemoryKindProject   = "project"   // project/working context
	MemoryKindReference = "reference" // reference material to retain
	MemoryKindFact      = "fact"      // discrete facts (surfaced by /facts)
)

// MemoryMetadata is a free-form string map persisted as JSON in a TEXT column.
type MemoryMetadata map[string]string

func (m *MemoryMetadata) Scan(src interface{}) error  { return JSONScan(m, src) }
func (m MemoryMetadata) Value() (driver.Value, error) { return JSONValue(m) }

// MemoryEmbedding is []float32 persisted as JSON in a TEXT column. database/sql
// can't bind a bare []float32, so — like StringSlice — it carries its own
// Scanner/Valuer so it round-trips on every driver (SQLite, Postgres, MySQL).
type MemoryEmbedding []float32

func (e *MemoryEmbedding) Scan(src interface{}) error { return JSONScan(e, src) }
func (e MemoryEmbedding) Value() (driver.Value, error) {
	if e == nil {
		return "[]", nil
	}
	return JSONValue(e)
}

// Memory is one stored memory. Owner+Name is the primary key; UserId is the
// per-user scoping column (column name "user_id" — never "user", which is a
// reserved word in Postgres).
type Memory struct {
	Owner       string          `db:"pk" json:"owner"`
	Name        string          `db:"pk" json:"name"`
	UserId      string          `json:"userId"`
	CreatedTime string          `json:"createdTime"`
	UpdatedTime string          `json:"updatedTime"`
	Kind        string          `json:"kind"`
	Content     string          `json:"content"`
	Metadata    MemoryMetadata  `json:"metadata"`
	Embedding   MemoryEmbedding `json:"embedding"`
	Dimension   int             `json:"dimension"`
	Score       float32         `db:"-" json:"score"`
}

// GetId returns the owner/name identifier.
func (m *Memory) GetId() string {
	return fmt.Sprintf("%s/%s", m.Owner, m.Name)
}

// NormalizeMemoryKind returns a valid kind, defaulting unknown/empty to "user".
func NormalizeMemoryKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case MemoryKindUser, MemoryKindFeedback, MemoryKindProject, MemoryKindReference, MemoryKindFact:
		return strings.ToLower(strings.TrimSpace(kind))
	default:
		return MemoryKindUser
	}
}

// NewMemoryName mints a unique per-owner memory name.
func NewMemoryName() string {
	return fmt.Sprintf("memory_%s", util.GetRandomName())
}

// scopeExp builds the (owner, userId[, kind]) where-clause shared by every
// scoped query. kind is optional.
func scopeExp(owner, userId, kind string) dbx.HashExp {
	exp := dbx.HashExp{"owner": owner, "user_id": userId}
	if kind != "" {
		exp["kind"] = kind
	}
	return exp
}

// store is the memory table's handle, or an error naming its absence.
//
// The adapter is nil before the DB is initialised — during boot, in the
// standalone runtime with no driverName, and in every unit test — so reading
// through it turns a missing dependency into a SIGSEGV at whatever call site
// asked first. Every accessor below asks here, so that call site is this one
// rather than whichever read happened to run.
func store() (*dbx.DB, error) {
	if adapter == nil || adapter.db == nil {
		return nil, fmt.Errorf("memory store is not initialised")
	}
	return adapter.db, nil
}

// listMemoriesScoped returns the caller's memories, newest first, optionally
// filtered by kind.
func listMemoriesScoped(owner, userId, kind string) ([]*Memory, error) {
	memories := []*Memory{}
	if owner == "" || userId == "" {
		return memories, nil
	}
	handle, err := store()
	if err != nil {
		return memories, err
	}
	err = findAll(handle, "memory", &memories, scopeExp(owner, userId, kind), "created_time DESC")
	if err != nil {
		return memories, err
	}
	return memories, nil
}

// GetMemories returns all of the caller's memories, newest first.
func GetMemories(owner, userId string) ([]*Memory, error) {
	return listMemoriesScoped(owner, userId, "")
}

// RecallMemories returns the caller's most recent memories (optionally a single
// kind), capped at limit.
func RecallMemories(owner, userId, kind string, limit int) ([]*Memory, error) {
	if limit <= 0 {
		limit = 20
	}
	memories, err := listMemoriesScoped(owner, userId, kind)
	if err != nil {
		return memories, err
	}
	if len(memories) > limit {
		memories = memories[:limit]
	}
	return memories, nil
}

// GetFacts returns the caller's fact-kind memories, newest first, capped at limit.
func GetFacts(owner, userId string, limit int) ([]*Memory, error) {
	return RecallMemories(owner, userId, MemoryKindFact, limit)
}

// CountMemories counts the caller's memories.
func CountMemories(owner, userId string) (int64, error) {
	if owner == "" || userId == "" {
		return 0, nil
	}
	handle, err := store()
	if err != nil {
		return 0, err
	}
	return countWhere(handle, "memory", scopeExp(owner, userId, ""))
}

// getMemoryByName fetches a single row by primary key (owner, name) WITHOUT a
// user check. Internal only — callers must use GetMemoryScoped for any
// request-driven access.
func getMemoryByName(owner, name string) (*Memory, error) {
	memory := Memory{Owner: owner, Name: name}
	handle, err := store()
	if err != nil {
		return nil, err
	}
	existed, err := getOne(handle, "memory", &memory, pk2(owner, name))
	if err != nil {
		return nil, err
	}
	if existed {
		return &memory, nil
	}
	return nil, nil
}

// GetMemoryScoped fetches one memory and enforces ownership: it returns nil
// unless the row belongs to BOTH this owner and this userId. This is the guard
// that prevents same-org cross-user reads via a guessed name.
func GetMemoryScoped(owner, userId, name string) (*Memory, error) {
	memory, err := getMemoryByName(owner, name)
	if err != nil {
		return nil, err
	}
	if memory == nil || memory.UserId != userId {
		return nil, nil
	}
	return memory, nil
}

// GetMemoryByIdScoped resolves an owner/name id, enforces it matches the
// caller's org, then applies the user scope.
func GetMemoryByIdScoped(owner, userId, id string) (*Memory, error) {
	rowOwner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		// Bare name (no slash) is treated as belonging to the caller's org.
		name = id
		rowOwner = owner
	}
	if rowOwner != owner {
		return nil, nil
	}
	return GetMemoryScoped(owner, userId, name)
}

// AddMemory inserts a memory. The caller must have set Owner and UserId from the
// authenticated identity. Missing timestamps/name/kind are filled in here.
func AddMemory(memory *Memory) (bool, error) {
	if memory.Owner == "" || memory.UserId == "" {
		return false, fmt.Errorf("memory requires an owner and userId")
	}
	if memory.Name == "" {
		memory.Name = NewMemoryName()
	}
	if memory.CreatedTime == "" {
		memory.CreatedTime = util.GetCurrentTime()
	}
	if memory.UpdatedTime == "" {
		memory.UpdatedTime = memory.CreatedTime
	}
	memory.Kind = NormalizeMemoryKind(memory.Kind)
	memory.Dimension = len(memory.Embedding)
	handle, err := store()
	if err != nil {
		return false, err
	}
	err = insertRow(handle, memory)
	if err != nil {
		return false, err
	}
	return true, nil
}

// UpdateMemoryScoped applies a patch to the caller's memory. Returns false if
// the memory does not exist or is not owned by this user. When the content
// changes the embedding is recomputed (best-effort).
func UpdateMemoryScoped(owner, userId, name string, patch *Memory, lang string) (bool, error) {
	existing, err := GetMemoryScoped(owner, userId, name)
	if err != nil {
		return false, err
	}
	if existing == nil {
		return false, nil
	}
	contentChanged := false
	if patch.Content != "" && patch.Content != existing.Content {
		existing.Content = patch.Content
		contentChanged = true
	}
	if patch.Kind != "" {
		existing.Kind = NormalizeMemoryKind(patch.Kind)
	}
	if patch.Metadata != nil {
		existing.Metadata = patch.Metadata
	}
	if contentChanged {
		EmbedMemory(existing, lang)
	}
	existing.UpdatedTime = util.GetCurrentTime()
	// Owner+Name is the PK; existing was already user-verified above.
	handle, err := store()
	if err != nil {
		return false, err
	}
	err = handle.Model(existing).Update()
	if err != nil {
		return false, err
	}
	return true, nil
}

// DeleteMemoryScoped removes the caller's memory. The where-clause includes
// user_id, so a caller can never delete another user's memory.
func DeleteMemoryScoped(owner, userId, name string) (bool, error) {
	if owner == "" || userId == "" || name == "" {
		return false, nil
	}
	handle, err := store()
	if err != nil {
		return false, err
	}
	affected, err := deleteWhere(handle, "memory",
		dbx.HashExp{"owner": owner, "name": name, "user_id": userId})
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

// embedMemoryText computes an embedding for text using the org's configured
// embedding provider (the same one stores/RAG use). Best-effort: any failure
// (no provider configured, transient error) returns (nil, err) and callers
// fall back to text search.
func embedMemoryText(owner, text, lang string) ([]float32, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	_, embeddingProviderObj, err := GetEmbeddingProviderFromContext(owner, "", lang)
	if err != nil || embeddingProviderObj == nil {
		return nil, err
	}
	vector, _, err := queryVectorSafe(embeddingProviderObj, text, lang)
	if err != nil {
		return nil, err
	}
	return vector, nil
}

// EmbedMemory fills in Embedding/Dimension best-effort. On failure the memory is
// stored without an embedding and remains searchable by text.
func EmbedMemory(memory *Memory, lang string) {
	vector, err := embedMemoryText(memory.Owner, memory.Content, lang)
	if err != nil || len(vector) == 0 {
		memory.Embedding = nil
		memory.Dimension = 0
		return
	}
	memory.Embedding = MemoryEmbedding(vector)
	memory.Dimension = len(vector)
}

// SearchMemories returns the caller's memories most relevant to query. If the
// embedding provider is available and candidates carry embeddings of matching
// dimension, results are ranked by cosine similarity; otherwise it falls back to
// a case-insensitive text match. Always scoped to (owner, userId).
func SearchMemories(owner, userId, query, kind string, limit int, lang string) ([]*Memory, error) {
	if limit <= 0 {
		limit = 20
	}
	candidates, err := listMemoriesScoped(owner, userId, kind)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return []*Memory{}, nil
	}
	if strings.TrimSpace(query) == "" {
		if len(candidates) > limit {
			candidates = candidates[:limit]
		}
		return candidates, nil
	}

	// Semantic path: embed the query, rank candidates that carry a
	// same-dimension embedding by cosine similarity.
	if queryVector, _ := embedMemoryText(owner, query, lang); len(queryVector) > 0 {
		embedded := [][]float32{}
		embeddedIdx := []int{}
		for i, m := range candidates {
			if len(m.Embedding) == len(queryVector) {
				embedded = append(embedded, []float32(m.Embedding))
				embeddedIdx = append(embeddedIdx, i)
			}
		}
		if len(embedded) > 0 {
			similarities, simErr := getNearestVectors(queryVector, embedded, limit)
			if simErr == nil {
				results := make([]*Memory, 0, len(similarities))
				for _, sim := range similarities {
					m := candidates[embeddedIdx[sim.Index]]
					m.Score = sim.Similarity
					results = append(results, m)
				}
				return results, nil
			}
		}
	}

	// Text fallback: substring match on content + metadata values, newest first.
	return textSearchMemories(candidates, query, limit), nil
}

// textSearchMemories filters candidates (already newest-first) by a
// case-insensitive substring match against content and metadata values.
func textSearchMemories(candidates []*Memory, query string, limit int) []*Memory {
	needle := strings.ToLower(strings.TrimSpace(query))
	results := make([]*Memory, 0, limit)
	for _, m := range candidates {
		if len(results) >= limit {
			break
		}
		if memoryMatchesText(m, needle) {
			m.Score = 1.0
			results = append(results, m)
		}
	}
	return results
}

func memoryMatchesText(m *Memory, needle string) bool {
	if needle == "" {
		return true
	}
	if strings.Contains(strings.ToLower(m.Content), needle) {
		return true
	}
	for _, v := range m.Metadata {
		if strings.Contains(strings.ToLower(v), needle) {
			return true
		}
	}
	return false
}
