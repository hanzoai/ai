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

package object

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/hanzoai/ai/conf"
	"github.com/klauspost/compress/zstd"
)

// Sentinel errors returned by ThreadStore operations.
var (
	ErrThreadNotFound  = errors.New("thread not found")
	ErrBranchNotFound  = errors.New("branch not found")
	ErrBranchExists    = errors.New("branch already exists")
	ErrChunkNotFound   = errors.New("chunk not found")
	ErrLeaseConflict   = errors.New("lease epoch conflict")
	ErrForbidden       = errors.New("forbidden")
	ErrInvalidGrant    = errors.New("invalid grant")
	ErrInvalidArgument = errors.New("invalid argument")
)

// Canonical capability scopes for cross-org / cross-user thread grants.
const (
	ThreadScopeRead    = "read"
	ThreadScopeAppend  = "append"
	ThreadScopeFork    = "fork"
	ThreadScopeHandoff = "handoff"
	ThreadScopeMerge   = "merge"
)

const (
	// DefaultBranchName is the initial branch created on every thread.
	DefaultBranchName = "main"
	// DefaultLeaseTTL is used when a handoff does not specify a TTL.
	DefaultLeaseTTL = time.Hour
)

// ThreadToolCall represents a tool/function call stored inside an assistant Chunk.
type ThreadToolCall struct {
	ID        string `json:"id"`
	Type      string `json:"type,omitempty"`
	Name      string `json:"name"`
	Arguments string `json:"arguments,omitempty"`
}

// HandoffEvent records an immutable atomic lease transfer between agents,
// machines, or accounts on a thread branch.
type HandoffEvent struct {
	FromEpoch    int64  `json:"from_epoch"`
	ToEpoch      int64  `json:"to_epoch"`
	FromActor    string `json:"from_actor,omitempty"`
	FromOrg      string `json:"from_org,omitempty"`
	ToActor      string `json:"to_actor"`
	ToOrg        string `json:"to_org"`
	ToMachine    string `json:"to_machine,omitempty"`
	WorkspaceRef string `json:"workspace_ref,omitempty"`
	GitRef       string `json:"git_ref,omitempty"`
	ModelHint    string `json:"model_hint,omitempty"`
	Note         string `json:"note,omitempty"`
}

// Chunk is an immutable, content-addressed node in a 10M+ thread DAG.
// Its ID is deterministic: "chk_" + first 16 hex chars of SHA-256 over its
// canonical payload and parent link.
type Chunk struct {
	ID         string            `json:"id"`
	ParentID   string            `json:"parent_id,omitempty"`
	ResponseID string            `json:"response_id,omitempty"`
	Kind       string            `json:"kind,omitempty"` // "message" (default) | "handoff" | "merge" | "cold_index"
	Role       string            `json:"role"`
	Content    string            `json:"content,omitempty"`
	ToolCalls  []ThreadToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
	Pinned     bool              `json:"pinned,omitempty"`
	Tokens     int               `json:"tokens"`
	Actor      string            `json:"actor,omitempty"`
	Org        string            `json:"org,omitempty"`
	Model      string            `json:"model,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	Handoff    *HandoffEvent     `json:"handoff,omitempty"`
	CreatedAt  time.Time         `json:"created_at"`
}

// ThreadLease is a fenced single-writer lock on a thread branch. Optimistic CAS on
// Epoch prevents distributed agents from racing on the same branch.
type ThreadLease struct {
	Holder       string    `json:"holder"`
	Actor        string    `json:"actor"`
	Org          string    `json:"org"`
	MachineID    string    `json:"machine_id,omitempty"`
	Epoch        int64     `json:"epoch"`
	WorkspaceRef string    `json:"workspace_ref,omitempty"`
	GitRef       string    `json:"git_ref,omitempty"`
	ModelHint    string    `json:"model_hint,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// Grant confers explicit, optionally expiring capabilities on a thread to a
// cross-org or cross-user principal ("org:<org>" or "user:<org>/<user>").
// Per-turn inference billing and API keys always stay on the executing caller.
type Grant struct {
	Principal string    `json:"principal"`
	Scopes    []string  `json:"scopes"`
	GrantedBy string    `json:"granted_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

// Branch is a named, O(1)-forkable head over immutable content-addressed chunks.
type Branch struct {
	Name         string    `json:"name"`
	HeadChunkID  string    `json:"head_chunk_id,omitempty"`
	ParentBranch string    `json:"parent_branch,omitempty"`
	ForkChunkID  string    `json:"fork_chunk_id,omitempty"`
	ChunkIDs     []string  `json:"chunk_ids"`
	TotalTokens  int          `json:"total_tokens"`
	Lease        *ThreadLease `json:"lease,omitempty"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
}

// Thread is a durable multi-branch conversation DAG supporting 10M+ tokens,
// fenced multi-agent handoffs, and multi-account capability ACLs.
type Thread struct {
	ID          string             `json:"id"`
	Org         string             `json:"org"`
	User        string             `json:"user,omitempty"`
	Title       string             `json:"title,omitempty"`
	Metadata    map[string]string  `json:"metadata,omitempty"`
	Branches    map[string]*Branch `json:"branches"`
	Grants      map[string]*Grant  `json:"grants,omitempty"`
	TotalTokens int                `json:"total_tokens"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
}

// ChunkSlice is the result of a lossless byte-range read from the chunk store.
type ChunkSlice struct {
	Chunk      Chunk  `json:"chunk"`
	Content    string `json:"content"`
	ByteOffset int    `json:"byte_offset"`
	ByteLimit  int    `json:"byte_limit"`
	TotalBytes int    `json:"total_bytes"`
	NextOffset int    `json:"next_offset"`
	HasMore    bool   `json:"has_more"`
}

// ResponseRef maps an OpenAI Responses ID (resp_...) back to its thread branch chunk.
type ResponseRef struct {
	ThreadID string `json:"thread_id"`
	Branch   string `json:"branch"`
	ChunkID  string `json:"chunk_id"`
}

// ChunkBlobBackend stores and loads immutable serialized chunk payloads by chunk ID.
type ChunkBlobBackend interface {
	Put(chunkID string, data []byte) error
	Get(chunkID string) ([]byte, error)
	Has(chunkID string) bool
}

// MemChunkBlobBackend is an in-memory ChunkBlobBackend for tests and ephemeral stores.
type MemChunkBlobBackend struct {
	mu    sync.RWMutex
	blobs map[string][]byte
}

// NewMemChunkBlobBackend constructs an empty in-memory blob backend.
func NewMemChunkBlobBackend() *MemChunkBlobBackend {
	return &MemChunkBlobBackend{blobs: make(map[string][]byte)}
}

func (m *MemChunkBlobBackend) Put(chunkID string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.blobs[chunkID]; exists {
		return nil // content-addressed: immutable once written
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	m.blobs[chunkID] = cp
	return nil
}

func (m *MemChunkBlobBackend) Get(chunkID string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.blobs[chunkID]
	if !ok {
		return nil, ErrChunkNotFound
	}
	cp := make([]byte, len(b))
	copy(cp, b)
	return cp, nil
}

func (m *MemChunkBlobBackend) Has(chunkID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.blobs[chunkID]
	return ok
}

// DiskChunkBlobBackend persists content-addressed chunks as zstd-compressed files
// sharded by prefix under <dir>/chunks/<shard>/<chunkID>.zst.
type DiskChunkBlobBackend struct {
	dir string
	enc *zstd.Encoder
	dec *zstd.Decoder
}

// NewDiskChunkBlobBackend creates a zstd-compressed content-addressed blob backend at dir.
func NewDiskChunkBlobBackend(dir string) (*DiskChunkBlobBackend, error) {
	if err := os.MkdirAll(filepath.Join(dir, "chunks"), 0o755); err != nil {
		return nil, err
	}
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return nil, err
	}
	dec, err := zstd.NewReader(nil)
	if err != nil {
		enc.Close()
		return nil, err
	}
	return &DiskChunkBlobBackend{dir: dir, enc: enc, dec: dec}, nil
}

func (d *DiskChunkBlobBackend) chunkPath(chunkID string) (string, error) {
	clean := strings.TrimSpace(chunkID)
	if clean == "" || strings.ContainsAny(clean, `/\..`) {
		return "", ErrInvalidArgument
	}
	shard := "00"
	if len(clean) >= 6 {
		shard = clean[4:6]
	}
	return filepath.Join(d.dir, "chunks", shard, clean+".zst"), nil
}

func (d *DiskChunkBlobBackend) Put(chunkID string, data []byte) error {
	target, err := d.chunkPath(chunkID)
	if err != nil {
		return err
	}
	if _, err := os.Stat(target); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	compressed := d.enc.EncodeAll(data, make([]byte, 0, len(data)/2+64))
	tmp := target + ".tmp." + uuid.NewString()
	if err := os.WriteFile(tmp, compressed, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (d *DiskChunkBlobBackend) Get(chunkID string) ([]byte, error) {
	target, err := d.chunkPath(chunkID)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrChunkNotFound
		}
		return nil, err
	}
	return d.dec.DecodeAll(raw, nil)
}

func (d *DiskChunkBlobBackend) Has(chunkID string) bool {
	target, err := d.chunkPath(chunkID)
	if err != nil {
		return false
	}
	_, err = os.Stat(target)
	return err == nil
}

// ThreadStore manages threads, branches, content-addressed chunks, fenced
// leases, capability grants, and response-ID continuations.
type ThreadStore struct {
	mu        sync.RWMutex
	dir       string // empty for pure in-memory store
	blobs     ChunkBlobBackend
	threads   map[string]*Thread
	responses map[string]ResponseRef
	nowFn     func() time.Time
}

// NewMemoryThreadStore creates a thread-safe in-memory ThreadStore (ideal for tests).
func NewMemoryThreadStore() *ThreadStore {
	return &ThreadStore{
		blobs:     NewMemChunkBlobBackend(),
		threads:   make(map[string]*Thread),
		responses: make(map[string]ResponseRef),
		nowFn:     time.Now,
	}
}

// NewDiskThreadStore opens or initializes a durable ThreadStore backed by
// zstd-compressed chunk blobs and atomic JSON metadata files under dataDir.
func NewDiskThreadStore(dataDir string) (*ThreadStore, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, ErrInvalidArgument
	}
	blobs, err := NewDiskChunkBlobBackend(dataDir)
	if err != nil {
		return nil, err
	}
	threadsDir := filepath.Join(dataDir, "threads")
	if err := os.MkdirAll(threadsDir, 0o755); err != nil {
		return nil, err
	}
	s := &ThreadStore{
		dir:       dataDir,
		blobs:     blobs,
		threads:   make(map[string]*Thread),
		responses: make(map[string]ResponseRef),
		nowFn:     time.Now,
	}
	if err := s.loadFromDisk(); err != nil {
		return nil, err
	}
	return s, nil
}

// NewThreadStoreWithBackend constructs a ThreadStore with a custom blob backend.
func NewThreadStoreWithBackend(blobs ChunkBlobBackend) *ThreadStore {
	if blobs == nil {
		blobs = NewMemChunkBlobBackend()
	}
	return &ThreadStore{
		blobs:     blobs,
		threads:   make(map[string]*Thread),
		responses: make(map[string]ResponseRef),
		nowFn:     time.Now,
	}
}

func (s *ThreadStore) loadFromDisk() error {
	threadsDir := filepath.Join(s.dir, "threads")
	entries, err := os.ReadDir(threadsDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(threadsDir, entry.Name()))
		if err != nil {
			return err
		}
		var thr Thread
		if err := json.Unmarshal(raw, &thr); err != nil {
			continue
		}
		if thr.ID == "" {
			continue
		}
		if thr.Branches == nil {
			thr.Branches = make(map[string]*Branch)
		}
		if thr.Grants == nil {
			thr.Grants = make(map[string]*Grant)
		}
		s.threads[thr.ID] = &thr
	}
	respPath := filepath.Join(s.dir, "responses.json")
	if raw, err := os.ReadFile(respPath); err == nil {
		_ = json.Unmarshal(raw, &s.responses)
	}
	return nil
}

func (s *ThreadStore) persistThreadLocked(thr *Thread) error {
	if s.dir == "" {
		return nil
	}
	raw, err := json.Marshal(thr)
	if err != nil {
		return err
	}
	target := filepath.Join(s.dir, "threads", thr.ID+".json")
	tmp := target + ".tmp." + uuid.NewString()
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (s *ThreadStore) persistResponsesLocked() error {
	if s.dir == "" {
		return nil
	}
	raw, err := json.Marshal(s.responses)
	if err != nil {
		return err
	}
	target := filepath.Join(s.dir, "responses.json")
	tmp := target + ".tmp." + uuid.NewString()
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

var (
	defaultThreadStoreMu sync.RWMutex
	defaultThreadStore   *ThreadStore
)

// DefaultThreadStore returns the process-wide ThreadStore singleton, lazily
// initializing a disk-backed store when HANZO_THREADS_DIR is configured or an
// in-memory store otherwise.
func DefaultThreadStore() *ThreadStore {
	defaultThreadStoreMu.RLock()
	s := defaultThreadStore
	defaultThreadStoreMu.RUnlock()
	if s != nil {
		return s
	}
	defaultThreadStoreMu.Lock()
	defer defaultThreadStoreMu.Unlock()
	if defaultThreadStore != nil {
		return defaultThreadStore
	}
	if dir := strings.TrimSpace(conf.GetConfigString("HANZO_THREADS_DIR")); dir != "" {
		if ds, err := NewDiskThreadStore(dir); err == nil {
			defaultThreadStore = ds
			return defaultThreadStore
		}
	}
	defaultThreadStore = NewMemoryThreadStore()
	return defaultThreadStore
}

// SetDefaultThreadStore replaces the process-wide ThreadStore (useful for tests
// and startup initialization).
func SetDefaultThreadStore(s *ThreadStore) {
	defaultThreadStoreMu.Lock()
	defaultThreadStore = s
	defaultThreadStoreMu.Unlock()
}

// EstimateTokens provides a fast, deterministic token count heuristic (~4 chars/token)
// with a minimum of 1 token for non-empty text.
func EstimateTokens(text string) int {
	if len(text) == 0 {
		return 0
	}
	n := (len(text) + 3) / 4
	if n < 1 {
		return 1
	}
	return n
}

// ChunkTokens returns the token count of a chunk, estimating it when Tokens <= 0.
func ChunkTokens(c Chunk) int {
	if c.Tokens > 0 {
		return c.Tokens
	}
	total := EstimateTokens(c.Content) + 4 // role/framing overhead
	for _, tc := range c.ToolCalls {
		total += EstimateTokens(tc.Name) + EstimateTokens(tc.Arguments) + EstimateTokens(tc.ID) + 6
	}
	if c.ToolCallID != "" {
		total += EstimateTokens(c.ToolCallID) + 2
	}
	if c.Handoff != nil {
		total += EstimateTokens(c.Handoff.Note) + 12
	}
	return total
}

// computeChunkID derives the deterministic content-addressed chunk ID ("chk_<sha256_16>").
func computeChunkID(c Chunk) string {
	type canonical struct {
		ParentID   string            `json:"parent_id,omitempty"`
		ResponseID string            `json:"response_id,omitempty"`
		Kind       string            `json:"kind,omitempty"`
		Role       string            `json:"role"`
		Content    string            `json:"content,omitempty"`
		ToolCalls  []ThreadToolCall  `json:"tool_calls,omitempty"`
		ToolCallID string            `json:"tool_call_id,omitempty"`
		Pinned     bool              `json:"pinned,omitempty"`
		Actor      string            `json:"actor,omitempty"`
		Org        string            `json:"org,omitempty"`
		Model      string            `json:"model,omitempty"`
		Metadata   map[string]string `json:"metadata,omitempty"`
		Handoff    *HandoffEvent     `json:"handoff,omitempty"`
		CreatedAt  int64             `json:"created_at_ns"`
	}
	raw, _ := json.Marshal(canonical{
		ParentID:   c.ParentID,
		ResponseID: c.ResponseID,
		Kind:       c.Kind,
		Role:       c.Role,
		Content:    c.Content,
		ToolCalls:  c.ToolCalls,
		ToolCallID: c.ToolCallID,
		Pinned:     c.Pinned,
		Actor:      c.Actor,
		Org:        c.Org,
		Model:      c.Model,
		Metadata:   c.Metadata,
		Handoff:    c.Handoff,
		CreatedAt:  c.CreatedAt.UTC().UnixNano(),
	})
	sum := sha256.Sum256(raw)
	return "chk_" + hex.EncodeToString(sum[:8])
}

// CreateThread creates a new thread owned by org/user with an initial "main" branch.
func (s *ThreadStore) CreateThread(org, user, title string, metadata map[string]string) (*Thread, error) {
	org = strings.TrimSpace(org)
	if org == "" {
		return nil, fmt.Errorf("%w: org is required", ErrInvalidArgument)
	}
	now := s.nowFn().UTC()
	id := "thr_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	metaCopy := cloneStringMap(metadata)
	thr := &Thread{
		ID:       id,
		Org:      org,
		User:     strings.TrimSpace(user),
		Title:    strings.TrimSpace(title),
		Metadata: metaCopy,
		Branches: map[string]*Branch{
			DefaultBranchName: {
				Name:      DefaultBranchName,
				ChunkIDs:  []string{},
				CreatedAt: now,
				UpdatedAt: now,
			},
		},
		Grants:    make(map[string]*Grant),
		CreatedAt: now,
		UpdatedAt: now,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.persistThreadLocked(thr); err != nil {
		return nil, err
	}
	s.threads[id] = thr
	return cloneThread(thr), nil
}

// GetThread returns a deep copy of the thread metadata, branches, leases, and grants.
func (s *ThreadStore) GetThread(threadID string) (*Thread, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	thr, ok := s.threads[threadID]
	if !ok {
		return nil, ErrThreadNotFound
	}
	return cloneThread(thr), nil
}

func (s *ThreadStore) putChunkLocked(c Chunk) (Chunk, error) {
	if c.Kind == "" {
		c.Kind = "message"
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = s.nowFn().UTC()
	}
	c.Tokens = ChunkTokens(c)
	c.ID = computeChunkID(c)
	raw, err := json.Marshal(c)
	if err != nil {
		return Chunk{}, err
	}
	if err := s.blobs.Put(c.ID, raw); err != nil {
		return Chunk{}, err
	}
	return c, nil
}

func (s *ThreadStore) loadChunk(chunkID string) (Chunk, error) {
	raw, err := s.blobs.Get(chunkID)
	if err != nil {
		return Chunk{}, err
	}
	var c Chunk
	if err := json.Unmarshal(raw, &c); err != nil {
		return Chunk{}, err
	}
	return c, nil
}

// AppendChunk appends an immutable content-addressed chunk to branch.
// If expectedEpoch is non-nil, the branch's current lease epoch MUST equal
// *expectedEpoch or ErrLeaseConflict is returned (optimistic CAS fencing).
func (s *ThreadStore) AppendChunk(threadID, branch string, expectedEpoch *int64, chunk Chunk) (Chunk, int64, error) {
	if branch == "" {
		branch = DefaultBranchName
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	thr, ok := s.threads[threadID]
	if !ok {
		return Chunk{}, 0, ErrThreadNotFound
	}
	br, ok := thr.Branches[branch]
	if !ok {
		return Chunk{}, 0, ErrBranchNotFound
	}
	currentEpoch := branchEpoch(br)
	if expectedEpoch != nil && *expectedEpoch != currentEpoch {
		return Chunk{}, currentEpoch, ErrLeaseConflict
	}

	chunk.ParentID = br.HeadChunkID
	stored, err := s.putChunkLocked(chunk)
	if err != nil {
		return Chunk{}, currentEpoch, err
	}

	now := s.nowFn().UTC()
	br.HeadChunkID = stored.ID
	br.ChunkIDs = append(br.ChunkIDs, stored.ID)
	br.TotalTokens += stored.Tokens
	br.UpdatedAt = now
	thr.UpdatedAt = now
	if branch == DefaultBranchName {
		thr.TotalTokens = br.TotalTokens
	} else {
		thr.TotalTokens = maxThreadTokensLocked(thr)
	}

	if stored.ResponseID != "" {
		s.responses[stored.ResponseID] = ResponseRef{
			ThreadID: threadID,
			Branch:   branch,
			ChunkID:  stored.ID,
		}
		_ = s.persistResponsesLocked()
	}
	if err := s.persistThreadLocked(thr); err != nil {
		return Chunk{}, currentEpoch, err
	}
	return stored, currentEpoch, nil
}

// Fork performs an O(1) branch fork from fromChunkID (or a response_id, or the
// source branch head if empty). Chunks are content-addressed and immutable, so
// no chunk blobs are copied.
func (s *ThreadStore) Fork(threadID, sourceBranch, fromChunkOrResponseID, newBranch string) (*Branch, error) {
	if sourceBranch == "" {
		sourceBranch = DefaultBranchName
	}
	newBranch = strings.TrimSpace(newBranch)
	if newBranch == "" {
		return nil, fmt.Errorf("%w: new branch name is required", ErrInvalidArgument)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	thr, ok := s.threads[threadID]
	if !ok {
		return nil, ErrThreadNotFound
	}
	if _, exists := thr.Branches[newBranch]; exists {
		return nil, ErrBranchExists
	}
	src, ok := thr.Branches[sourceBranch]
	if !ok {
		return nil, ErrBranchNotFound
	}

	targetChunkID := strings.TrimSpace(fromChunkOrResponseID)
	if targetChunkID != "" {
		if ref, ok := s.responses[targetChunkID]; ok && ref.ThreadID == threadID {
			targetChunkID = ref.ChunkID
		}
	}

	var forkedIDs []string
	var headID string
	var totalTokens int

	if targetChunkID == "" {
		forkedIDs = append([]string(nil), src.ChunkIDs...)
		headID = src.HeadChunkID
		totalTokens = src.TotalTokens
	} else {
		idx := slices.Index(src.ChunkIDs, targetChunkID)
		if idx < 0 {
			return nil, ErrChunkNotFound
		}
		forkedIDs = append([]string(nil), src.ChunkIDs[:idx+1]...)
		headID = targetChunkID
		for _, cid := range forkedIDs {
			if c, err := s.loadChunk(cid); err == nil {
				totalTokens += c.Tokens
			}
		}
	}

	now := s.nowFn().UTC()
	nb := &Branch{
		Name:         newBranch,
		HeadChunkID:  headID,
		ParentBranch: sourceBranch,
		ForkChunkID:  headID,
		ChunkIDs:     forkedIDs,
		TotalTokens:  totalTokens,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	thr.Branches[newBranch] = nb
	thr.UpdatedAt = now
	if err := s.persistThreadLocked(thr); err != nil {
		return nil, err
	}
	return cloneBranch(nb), nil
}

// Merge merges divergent chunks (or a concise merge summary when summary != "")
// from sourceBranch into targetBranch, checking optional expectedEpoch on
// targetBranch for CAS safety.
func (s *ThreadStore) Merge(threadID, sourceBranch, targetBranch, summary, actor, org string, expectedEpoch *int64) (Chunk, error) {
	sourceBranch = strings.TrimSpace(sourceBranch)
	if targetBranch == "" {
		targetBranch = DefaultBranchName
	}
	if sourceBranch == "" || sourceBranch == targetBranch {
		return Chunk{}, fmt.Errorf("%w: distinct source and target branches are required", ErrInvalidArgument)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	thr, ok := s.threads[threadID]
	if !ok {
		return Chunk{}, ErrThreadNotFound
	}
	src, ok := thr.Branches[sourceBranch]
	if !ok {
		return Chunk{}, ErrBranchNotFound
	}
	dst, ok := thr.Branches[targetBranch]
	if !ok {
		return Chunk{}, ErrBranchNotFound
	}
	currentEpoch := branchEpoch(dst)
	if expectedEpoch != nil && *expectedEpoch != currentEpoch {
		return Chunk{}, ErrLeaseConflict
	}

	// Find divergent chunks present on sourceBranch that are not yet on targetBranch.
	dstSet := make(map[string]bool, len(dst.ChunkIDs))
	for _, id := range dst.ChunkIDs {
		dstSet[id] = true
	}
	var divergent []string
	for _, id := range src.ChunkIDs {
		if !dstSet[id] {
			divergent = append(divergent, id)
		}
	}

	now := s.nowFn().UTC()
	// When no explicit summary is supplied, fast-forward/cherry-pick the divergent
	// chunks into targetBranch directly, then append a merge record chunk.
	if strings.TrimSpace(summary) == "" {
		for _, cid := range divergent {
			c, err := s.loadChunk(cid)
			if err != nil {
				continue
			}
			dst.ChunkIDs = append(dst.ChunkIDs, cid)
			dst.HeadChunkID = cid
			dst.TotalTokens += c.Tokens
		}
	}

	mergeContent := strings.TrimSpace(summary)
	if mergeContent == "" {
		mergeContent = fmt.Sprintf("Merged %d chunk(s) from branch %q into %q (head %s).", len(divergent), sourceBranch, targetBranch, src.HeadChunkID)
	}
	mergeChunk := Chunk{
		ParentID: dst.HeadChunkID,
		Kind:     "merge",
		Role:     "system",
		Content:  mergeContent,
		Actor:    actor,
		Org:      org,
		Metadata: map[string]string{
			"merge_source_branch": sourceBranch,
			"merge_target_branch": targetBranch,
			"merge_source_head":   src.HeadChunkID,
			"merged_chunk_count":  fmt.Sprintf("%d", len(divergent)),
		},
		CreatedAt: now,
	}
	stored, err := s.putChunkLocked(mergeChunk)
	if err != nil {
		return Chunk{}, err
	}
	dst.HeadChunkID = stored.ID
	dst.ChunkIDs = append(dst.ChunkIDs, stored.ID)
	dst.TotalTokens += stored.Tokens
	dst.UpdatedAt = now
	thr.UpdatedAt = now
	if targetBranch == DefaultBranchName {
		thr.TotalTokens = dst.TotalTokens
	} else {
		thr.TotalTokens = maxThreadTokensLocked(thr)
	}

	if err := s.persistThreadLocked(thr); err != nil {
		return Chunk{}, err
	}
	return stored, nil
}

// Handoff atomically verifies fromEpoch on branch, increments the lease epoch,
// transfers the lease to (toActor, toOrg, toMachine), and appends an immutable
// handoff event chunk to the branch DAG.
func (s *ThreadStore) Handoff(
	threadID, branch string,
	fromEpoch int64,
	toActor, toOrg, toMachine, workspaceRef, gitRef, modelHint, note string,
	ttl time.Duration,
) (*ThreadLease, Chunk, error) {
	if branch == "" {
		branch = DefaultBranchName
	}
	toActor = strings.TrimSpace(toActor)
	toOrg = strings.TrimSpace(toOrg)
	if toActor == "" && toOrg == "" {
		return nil, Chunk{}, fmt.Errorf("%w: to_actor or to_org is required", ErrInvalidArgument)
	}
	if ttl <= 0 {
		ttl = DefaultLeaseTTL
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	thr, ok := s.threads[threadID]
	if !ok {
		return nil, Chunk{}, ErrThreadNotFound
	}
	br, ok := thr.Branches[branch]
	if !ok {
		return nil, Chunk{}, ErrBranchNotFound
	}
	currentEpoch := branchEpoch(br)
	if fromEpoch != currentEpoch {
		return nil, Chunk{}, ErrLeaseConflict
	}

	var prevActor, prevOrg string
	if br.Lease != nil {
		prevActor = br.Lease.Actor
		prevOrg = br.Lease.Org
	}

	now := s.nowFn().UTC()
	newEpoch := currentEpoch + 1
	holder := toActor
	if toOrg != "" && toActor != "" {
		holder = toOrg + "/" + toActor
	} else if toOrg != "" {
		holder = toOrg
	}
	if toMachine != "" {
		holder += "@" + toMachine
	}

	lease := &ThreadLease{
		Holder:       holder,
		Actor:        toActor,
		Org:          toOrg,
		MachineID:    strings.TrimSpace(toMachine),
		Epoch:        newEpoch,
		WorkspaceRef: strings.TrimSpace(workspaceRef),
		GitRef:       strings.TrimSpace(gitRef),
		ModelHint:    strings.TrimSpace(modelHint),
		ExpiresAt:    now.Add(ttl),
	}

	ev := &HandoffEvent{
		FromEpoch:    currentEpoch,
		ToEpoch:      newEpoch,
		FromActor:    prevActor,
		FromOrg:      prevOrg,
		ToActor:      toActor,
		ToOrg:        toOrg,
		ToMachine:    lease.MachineID,
		WorkspaceRef: lease.WorkspaceRef,
		GitRef:       lease.GitRef,
		ModelHint:    lease.ModelHint,
		Note:         strings.TrimSpace(note),
	}

	content := ev.Note
	if content == "" {
		content = fmt.Sprintf("Handoff epoch %d -> %d to %s", currentEpoch, newEpoch, holder)
	}
	chunk := Chunk{
		ParentID:  br.HeadChunkID,
		Kind:      "handoff",
		Role:      "system",
		Content:   content,
		Actor:     toActor,
		Org:       toOrg,
		Model:     lease.ModelHint,
		Handoff:   ev,
		CreatedAt: now,
	}
	stored, err := s.putChunkLocked(chunk)
	if err != nil {
		return nil, Chunk{}, err
	}

	br.Lease = lease
	br.HeadChunkID = stored.ID
	br.ChunkIDs = append(br.ChunkIDs, stored.ID)
	br.TotalTokens += stored.Tokens
	br.UpdatedAt = now
	thr.UpdatedAt = now
	if branch == DefaultBranchName {
		thr.TotalTokens = br.TotalTokens
	}

	if err := s.persistThreadLocked(thr); err != nil {
		return nil, Chunk{}, err
	}
	return cloneLease(lease), stored, nil
}

// NormalizePrincipal canonicalizes a grant principal ("org:<org>" or "user:<org>/<user>").
func NormalizePrincipal(principal string) (string, bool) {
	p := strings.TrimSpace(principal)
	switch {
	case strings.HasPrefix(p, "org:"):
		org := strings.TrimSpace(strings.TrimPrefix(p, "org:"))
		if org == "" || strings.ContainsAny(org, "/ \t\r\n") {
			return "", false
		}
		return "org:" + strings.ToLower(org), true
	case strings.HasPrefix(p, "user:"):
		rest := strings.TrimSpace(strings.TrimPrefix(p, "user:"))
		org, user, ok := strings.Cut(rest, "/")
		org = strings.TrimSpace(org)
		user = strings.TrimSpace(user)
		if !ok || org == "" || user == "" || strings.ContainsAny(org+user, " \t\r\n") {
			return "", false
		}
		return "user:" + strings.ToLower(org) + "/" + strings.ToLower(user), true
	default:
		return "", false
	}
}

func validateScopes(scopes []string) ([]string, bool) {
	if len(scopes) == 0 {
		return nil, false
	}
	seen := make(map[string]bool, len(scopes))
	out := make([]string, 0, len(scopes))
	for _, sc := range scopes {
		s := strings.ToLower(strings.TrimSpace(sc))
		switch s {
		case ThreadScopeRead, ThreadScopeAppend, ThreadScopeFork, ThreadScopeHandoff, ThreadScopeMerge:
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		default:
			return nil, false
		}
	}
	return out, len(out) > 0
}

// AddGrant upserts a capability grant for a principal on threadID.
func (s *ThreadStore) AddGrant(threadID string, grant Grant) (Grant, error) {
	principal, ok := NormalizePrincipal(grant.Principal)
	if !ok {
		return Grant{}, fmt.Errorf("%w: principal must be org:<org> or user:<org>/<user>", ErrInvalidGrant)
	}
	scopes, ok := validateScopes(grant.Scopes)
	if !ok {
		return Grant{}, fmt.Errorf("%w: scopes must be a non-empty subset of read, append, fork, handoff, merge", ErrInvalidGrant)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	thr, ok := s.threads[threadID]
	if !ok {
		return Grant{}, ErrThreadNotFound
	}
	if thr.Grants == nil {
		thr.Grants = make(map[string]*Grant)
	}
	now := s.nowFn().UTC()
	g := &Grant{
		Principal: principal,
		Scopes:    scopes,
		GrantedBy: strings.TrimSpace(grant.GrantedBy),
		CreatedAt: now,
		ExpiresAt: grant.ExpiresAt.UTC(),
	}
	if grant.ExpiresAt.IsZero() {
		g.ExpiresAt = time.Time{}
	}
	thr.Grants[principal] = g
	thr.UpdatedAt = now
	if err := s.persistThreadLocked(thr); err != nil {
		return Grant{}, err
	}
	return *cloneGrant(g), nil
}

// RevokeGrant removes a capability grant for principal from threadID.
func (s *ThreadStore) RevokeGrant(threadID, principal string) error {
	norm, ok := NormalizePrincipal(principal)
	if !ok {
		return fmt.Errorf("%w: invalid principal %q", ErrInvalidGrant, principal)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	thr, ok := s.threads[threadID]
	if !ok {
		return ErrThreadNotFound
	}
	delete(thr.Grants, norm)
	thr.UpdatedAt = s.nowFn().UTC()
	return s.persistThreadLocked(thr)
}

// Can checks whether (callerOrg, callerUser) is authorized for scope on thread.
// The thread's owner org is always allowed; cross-account callers MUST hold a
// non-expired grant matching either "org:<callerOrg>" or "user:<callerOrg>/<callerUser>".
func Can(thread *Thread, callerOrg, callerUser, scope string) bool {
	return CanAt(thread, callerOrg, callerUser, scope, time.Now())
}

// CanAt is Can evaluated at a specific timestamp (for deterministic expiration tests).
func CanAt(thread *Thread, callerOrg, callerUser, scope string, now time.Time) bool {
	if thread == nil {
		return false
	}
	callerOrg = strings.TrimSpace(callerOrg)
	scope = strings.ToLower(strings.TrimSpace(scope))
	if callerOrg == "" || scope == "" {
		return false
	}
	// Owner org is always authorized.
	if strings.EqualFold(thread.Org, callerOrg) {
		return true
	}
	if len(thread.Grants) == 0 {
		return false
	}

	candidates := []string{"org:" + strings.ToLower(callerOrg)}
	u := strings.TrimSpace(callerUser)
	if u != "" {
		// Accept both bare "alice" and qualified "org/alice".
		if prefix, rest, hasSlash := strings.Cut(u, "/"); hasSlash && strings.EqualFold(prefix, callerOrg) {
			u = rest
		}
		if u != "" && !strings.Contains(u, "/") {
			candidates = append(candidates, "user:"+strings.ToLower(callerOrg)+"/"+strings.ToLower(u))
		}
	}

	for _, key := range candidates {
		g, ok := thread.Grants[key]
		if !ok || g == nil {
			continue
		}
		if !g.ExpiresAt.IsZero() && !now.Before(g.ExpiresAt) {
			continue // expired
		}
		if slices.Contains(g.Scopes, scope) {
			return true
		}
		// "append" implies "merge" unless caller asked for something else.
		if scope == ThreadScopeMerge && slices.Contains(g.Scopes, ThreadScopeAppend) {
			return true
		}
	}
	return false
}

// GetBranchChunks returns all immutable chunks along branch in chronological order.
func (s *ThreadStore) GetBranchChunks(threadID, branch string) ([]Chunk, error) {
	return s.GetBranchChunksUpTo(threadID, branch, "")
}

// GetBranchChunksUpTo returns all chunks on branch up to and including stopChunkID
// (or all chunks when stopChunkID is empty).
func (s *ThreadStore) GetBranchChunksUpTo(threadID, branch, stopChunkID string) ([]Chunk, error) {
	if branch == "" {
		branch = DefaultBranchName
	}
	s.mu.RLock()
	thr, ok := s.threads[threadID]
	if !ok {
		s.mu.RUnlock()
		return nil, ErrThreadNotFound
	}
	br, ok := thr.Branches[branch]
	if !ok {
		s.mu.RUnlock()
		return nil, ErrBranchNotFound
	}
	ids := append([]string(nil), br.ChunkIDs...)
	s.mu.RUnlock()

	if stopChunkID != "" {
		idx := slices.Index(ids, stopChunkID)
		if idx < 0 {
			return nil, ErrChunkNotFound
		}
		ids = ids[:idx+1]
	}

	out := make([]Chunk, 0, len(ids))
	for _, cid := range ids {
		c, err := s.loadChunk(cid)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// GetChunk performs a lossless byte-range read of chunkID belonging to threadID.
// When byteLimit <= 0, the remainder of the chunk from byteOffset is returned.
func (s *ThreadStore) GetChunk(threadID, chunkID string, byteOffset, byteLimit int) (*ChunkSlice, error) {
	s.mu.RLock()
	thr, ok := s.threads[threadID]
	if !ok {
		s.mu.RUnlock()
		return nil, ErrThreadNotFound
	}
	found := false
	for _, br := range thr.Branches {
		if slices.Contains(br.ChunkIDs, chunkID) {
			found = true
			break
		}
	}
	s.mu.RUnlock()
	if !found {
		return nil, ErrChunkNotFound
	}

	c, err := s.loadChunk(chunkID)
	if err != nil {
		return nil, err
	}
	totalBytes := len(c.Content)
	if byteOffset < 0 {
		byteOffset = 0
	}
	if byteOffset > totalBytes {
		byteOffset = totalBytes
	}
	end := totalBytes
	if byteLimit > 0 && byteOffset+byteLimit < end {
		end = byteOffset + byteLimit
	}
	sliceContent := c.Content[byteOffset:end]
	return &ChunkSlice{
		Chunk:      c,
		Content:    sliceContent,
		ByteOffset: byteOffset,
		ByteLimit:  byteLimit,
		TotalBytes: totalBytes,
		NextOffset: end,
		HasMore:    end < totalBytes,
	}, nil
}

// IndexResponse associates a response_id (resp_...) with a specific thread/branch/chunk.
func (s *ThreadStore) IndexResponse(responseID, threadID, branch, chunkID string) {
	if responseID == "" || threadID == "" || chunkID == "" {
		return
	}
	if branch == "" {
		branch = DefaultBranchName
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.responses[responseID] = ResponseRef{
		ThreadID: threadID,
		Branch:   branch,
		ChunkID:  chunkID,
	}
	_ = s.persistResponsesLocked()
}

// LookupResponse resolves a stored response_id back to its (threadID, branch, chunkID).
func (s *ThreadStore) LookupResponse(responseID string) (threadID, branch, chunkID string, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ref, exists := s.responses[strings.TrimSpace(responseID)]
	if !exists {
		return "", "", "", false
	}
	return ref.ThreadID, ref.Branch, ref.ChunkID, true
}

func branchEpoch(b *Branch) int64 {
	if b == nil || b.Lease == nil {
		return 0
	}
	return b.Lease.Epoch
}

func maxThreadTokensLocked(thr *Thread) int {
	maxTok := 0
	for _, b := range thr.Branches {
		if b.TotalTokens > maxTok {
			maxTok = b.TotalTokens
		}
	}
	return maxTok
}

func cloneStringMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneLease(l *ThreadLease) *ThreadLease {
	if l == nil {
		return nil
	}
	cp := *l
	return &cp
}

func cloneGrant(g *Grant) *Grant {
	if g == nil {
		return nil
	}
	cp := *g
	cp.Scopes = append([]string(nil), g.Scopes...)
	return &cp
}

func cloneBranch(b *Branch) *Branch {
	if b == nil {
		return nil
	}
	cp := *b
	cp.ChunkIDs = append([]string(nil), b.ChunkIDs...)
	cp.Lease = cloneLease(b.Lease)
	return &cp
}

func cloneThread(t *Thread) *Thread {
	if t == nil {
		return nil
	}
	cp := *t
	cp.Metadata = cloneStringMap(t.Metadata)
	cp.Branches = make(map[string]*Branch, len(t.Branches))
	for k, b := range t.Branches {
		cp.Branches[k] = cloneBranch(b)
	}
	cp.Grants = make(map[string]*Grant, len(t.Grants))
	for k, g := range t.Grants {
		cp.Grants[k] = cloneGrant(g)
	}
	return &cp
}
