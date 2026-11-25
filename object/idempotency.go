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
	"bytes"
	"container/list"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/dbx"
	kv "github.com/hanzokv/go/v9"
)

// Execution ledger states for idempotent mutations.
//
// Transitions:
//
//	CLAIMED  -> RUNNING   (before external side effects / inference start)
//	CLAIMED  -> COMPLETED (fast completion with no intermediate running stage)
//	CLAIMED  -> FAILED    (deterministic failure before side effects)
//	CLAIMED  -> CLAIMED   (safe lease takeover after lease_until expires, fence_token++)
//	RUNNING  -> COMPLETED (handler completed; response retained if within bounds & non-streaming)
//	RUNNING  -> FAILED    (handler failed; records whether side_effects_committed)
//	RUNNING  -> UNKNOWN   (ambiguous crash/timeout/panic after side effects started; fence_token++)
//	UNKNOWN  -> COMPLETED (explicit reconciliation only; fence_token++)
//	UNKNOWN  -> FAILED    (explicit reconciliation only; fence_token++)
const (
	IdemStateClaimed   = "CLAIMED"
	IdemStateRunning   = "RUNNING"
	IdemStateCompleted = "COMPLETED"
	IdemStateFailed    = "FAILED"
	IdemStateUnknown   = "UNKNOWN"
)

// Non-replayable outcome reasons stored when a completed execution cannot
// replay its original response body (and MUST NOT re-execute on retry).
const (
	IdemNonReplayOversized          = "oversized"
	IdemNonReplayStreaming          = "streaming"
	IdemNonReplayStreamDisconnected = "stream_disconnected"
)

// Sentinel errors returned by IdempotencyStore.
var (
	ErrIdempotencyStoreUnavailable    = errors.New("idempotency authoritative store unavailable")
	ErrIdempotencyConflict            = errors.New("idempotency key reused with different request parameters")
	ErrIdempotencyFenced              = errors.New("idempotency worker fenced out by newer lease or terminal state")
	ErrIdempotencyCardinalityExceeded = errors.New("idempotency active key limit exceeded for tenant")
	ErrIdempotencyInvalidKey          = errors.New("invalid idempotency key")
	ErrIdempotencyBodyTooLarge        = errors.New("request body exceeds idempotency fingerprint limit")
	ErrIdempotencyNotFound            = errors.New("idempotency record not found")
	ErrIdempotencyInvalidTransition   = errors.New("invalid idempotency state transition")
)

// IdempotencyRecord is the durable, authoritative execution ledger row stored
// in SQL (SQLite, PostgreSQL, or MySQL via dbx).
//
// The composite primary key (scope_hash, idempotency_key) enforces uniqueness
// across all replicas at the database level.
type IdempotencyRecord struct {
	ScopeHash      string `db:"pk" json:"scopeHash"`
	IdempotencyKey string `db:"pk" json:"idempotencyKey"`

	Org       string `json:"org"`
	Principal string `json:"principal"`
	Project   string `json:"project"`
	AuthScope string `json:"authScope"`
	Operation string `json:"operation"`

	RequestHash string `json:"requestHash"`

	// Immutable execution & billing identity minted once on initial claim and
	// preserved across retries, lease takeovers, and Commerce settlement calls.
	ExecutionID string `json:"executionId"`
	BillingRef  string `json:"billingRef"`

	State      string `json:"state"`
	FenceToken int64  `json:"fenceToken"`
	OwnerID    string `json:"ownerId"`
	LeaseUntil string `json:"leaseUntil"` // StampTime UTC

	SideEffectsStarted   bool `json:"sideEffectsStarted"`
	SideEffectsCommitted bool `json:"sideEffectsCommitted"`
	Streaming            bool `json:"streaming"`
	StreamDisconnected   bool `json:"streamDisconnected"`

	ResponseStatus   int    `json:"responseStatus"`
	ResponseHeaders  string `json:"responseHeaders"` // JSON object of bounded allowlisted headers
	ResponseBody     []byte `json:"responseBody"`    // strictly capped at MaxResponseBytes
	ResponseRetained bool   `json:"responseRetained"`
	NonReplayReason  string `json:"nonReplayReason"`

	ErrorCode    string `json:"errorCode"`
	ErrorMessage string `json:"errorMessage"`

	CreatedAt string `json:"createdAt"` // StampTime UTC
	UpdatedAt string `json:"updatedAt"` // StampTime UTC
	ExpiresAt string `json:"expiresAt"` // StampTime UTC
}

// IdempotencyScope identifies the IAM-verified caller and canonical operation.
// Org and Principal MUST come from verified IAM/in-process identity, never raw
// client-controlled headers.
type IdempotencyScope struct {
	Org       string
	Principal string
	Project   string
	AuthScope string
	Operation string
}

// IdempotencyClaimRequest describes an attempt to claim or replay an idempotent mutation.
type IdempotencyClaimRequest struct {
	Scope          IdempotencyScope
	IdempotencyKey string
	RequestHash    string
	OwnerID        string
	Streaming      bool
}

// IdempotencyClaimOutcome classifies the result of Claim.
type IdempotencyClaimOutcome int

const (
	ClaimOwned IdempotencyClaimOutcome = iota
	ClaimCompleted
	ClaimFailed
	ClaimInFlight
	ClaimUnknown
)

// IdempotencyClaimResult is returned by IdempotencyStore.Claim.
type IdempotencyClaimResult struct {
	Outcome IdempotencyClaimOutcome
	Record  *IdempotencyRecord
}

// IdempotencyOutcome carries the final response & side-effect metadata to persist
// when transitioning an execution to COMPLETED or FAILED.
type IdempotencyOutcome struct {
	Status               int
	Headers              map[string]string
	Body                 []byte
	Streaming            bool
	StreamDisconnected   bool
	SideEffectsCommitted bool
	ErrorCode            string
	ErrorMessage         string
}

// IdempotencyConfig bounds memory, payload sizes, leases, and tenant cardinality.
type IdempotencyConfig struct {
	LeaseTTL                time.Duration
	RecordTTL               time.Duration
	InFlightWaitTimeout     time.Duration
	MaxKeyLength            int
	MaxFingerprintBodyBytes int
	MaxResponseBytes        int
	MaxHeaderBytes          int
	MaxKeysPerTenant        int
	CacheMaxBytes           int64
	Now                     func() time.Time
}

// DefaultIdempotencyConfig returns production-safe bounds.
func DefaultIdempotencyConfig() IdempotencyConfig {
	return IdempotencyConfig{
		LeaseTTL:                30 * time.Second,
		RecordTTL:               24 * time.Hour,
		InFlightWaitTimeout:     5 * time.Second,
		MaxKeyLength:            256,
		MaxFingerprintBodyBytes: 4 * 1024 * 1024,  // 4 MiB
		MaxResponseBytes:        512 * 1024,       // 512 KiB
		MaxHeaderBytes:          16 * 1024,        // 16 KiB
		MaxKeysPerTenant:        10000,            // active unexpired rows per org
		CacheMaxBytes:           64 * 1024 * 1024, // 64 MiB L1 hot replay cache
		Now:                     time.Now,
	}
}

func (c IdempotencyConfig) normalized() IdempotencyConfig {
	d := DefaultIdempotencyConfig()
	if c.LeaseTTL <= 0 {
		c.LeaseTTL = d.LeaseTTL
	}
	if c.RecordTTL <= 0 {
		c.RecordTTL = d.RecordTTL
	}
	if c.InFlightWaitTimeout < 0 {
		c.InFlightWaitTimeout = 0
	}
	if c.MaxKeyLength <= 0 {
		c.MaxKeyLength = d.MaxKeyLength
	}
	if c.MaxFingerprintBodyBytes <= 0 {
		c.MaxFingerprintBodyBytes = d.MaxFingerprintBodyBytes
	}
	if c.MaxResponseBytes <= 0 {
		c.MaxResponseBytes = d.MaxResponseBytes
	}
	if c.MaxHeaderBytes <= 0 {
		c.MaxHeaderBytes = d.MaxHeaderBytes
	}
	if c.MaxKeysPerTenant <= 0 {
		c.MaxKeysPerTenant = d.MaxKeysPerTenant
	}
	if c.CacheMaxBytes <= 0 {
		c.CacheMaxBytes = d.CacheMaxBytes
	}
	if c.Now == nil {
		c.Now = d.Now
	}
	return c
}

// ── Context propagation for billing & side-effect tracking ───────────────────

type idempotencyExecCtxKey struct{}

// IdempotencyExecution is attached to the Go request context for any request
// claimed by IdempotencyFilter. Downstream controllers and billing code read
// ExecutionID and BillingRef so every debit/settlement carries one immutable
// identity across retries and lease recoveries.
type IdempotencyExecution struct {
	ScopeHash      string
	IdempotencyKey string
	ExecutionID    string
	BillingRef     string
	FenceToken     int64

	store *IdempotencyStore

	sideEffectsCommitted atomic.Bool
	streamDisconnected   atomic.Bool
}

// NewIdempotencyExecution constructs an active execution handle backed by store.
func NewIdempotencyExecution(store *IdempotencyStore, rec *IdempotencyRecord) *IdempotencyExecution {
	if rec == nil {
		return nil
	}
	e := &IdempotencyExecution{
		ScopeHash:      rec.ScopeHash,
		IdempotencyKey: rec.IdempotencyKey,
		ExecutionID:    rec.ExecutionID,
		BillingRef:     rec.BillingRef,
		FenceToken:     rec.FenceToken,
		store:          store,
	}
	if rec.SideEffectsCommitted {
		e.sideEffectsCommitted.Store(true)
	}
	if rec.StreamDisconnected {
		e.streamDisconnected.Store(true)
	}
	return e
}

// WithIdempotencyExecution binds exec onto ctx.
func WithIdempotencyExecution(ctx context.Context, exec *IdempotencyExecution) context.Context {
	if ctx == nil || exec == nil {
		return ctx
	}
	return context.WithValue(ctx, idempotencyExecCtxKey{}, exec)
}

// IdempotencyExecutionFromContext returns the active execution on ctx, or nil.
func IdempotencyExecutionFromContext(ctx context.Context) *IdempotencyExecution {
	if ctx == nil {
		return nil
	}
	exec, _ := ctx.Value(idempotencyExecCtxKey{}).(*IdempotencyExecution)
	return exec
}

// MarkSideEffectsCommitted durably marks that an irreversible external side
// effect or billing debit has occurred for this execution.
func (e *IdempotencyExecution) MarkSideEffectsCommitted(ctx context.Context) error {
	if e == nil {
		return nil
	}
	e.sideEffectsCommitted.Store(true)
	if e.store == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return e.store.MarkSideEffectsCommitted(ctx, e.ScopeHash, e.IdempotencyKey, e.FenceToken)
}

// IsSideEffectsCommitted reports whether side effects were marked committed.
func (e *IdempotencyExecution) IsSideEffectsCommitted() bool {
	if e == nil {
		return false
	}
	return e.sideEffectsCommitted.Load()
}

// MarkStreamDisconnected durably marks that the client disconnected mid-stream
// while preserving the execution & billing IDs.
func (e *IdempotencyExecution) MarkStreamDisconnected(ctx context.Context) error {
	if e == nil {
		return nil
	}
	e.streamDisconnected.Store(true)
	if e.store == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return e.store.MarkStreamDisconnected(ctx, e.ScopeHash, e.IdempotencyKey, e.FenceToken)
}

// IsStreamDisconnected reports whether a streaming disconnect was recorded.
func (e *IdempotencyExecution) IsStreamDisconnected() bool {
	if e == nil {
		return false
	}
	return e.streamDisconnected.Load()
}

// ── Length-prefixed canonical framing & fingerprinting ───────────────────────

// FrameHash hashes parts with explicit 8-byte big-endian length prefixes so
// distinct tuples can never collide regardless of embedded delimiters.
func FrameHash(parts ...[]byte) string {
	h := sha256.New()
	var lenBuf [8]byte
	for _, p := range parts {
		binary.BigEndian.PutUint64(lenBuf[:], uint64(len(p)))
		_, _ = h.Write(lenBuf[:])
		if len(p) > 0 {
			_, _ = h.Write(p)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ValidateIdempotencyKey checks key length and forbids empty/whitespace-only or
// ASCII control characters (preventing header/log injection and storage abuse).
func ValidateIdempotencyKey(key string, maxLen int) error {
	if maxLen <= 0 {
		maxLen = DefaultIdempotencyConfig().MaxKeyLength
	}
	if key == "" || strings.TrimSpace(key) == "" {
		return fmt.Errorf("%w: empty key", ErrIdempotencyInvalidKey)
	}
	if len(key) > maxLen {
		return fmt.Errorf("%w: length %d exceeds max %d", ErrIdempotencyInvalidKey, len(key), maxLen)
	}
	for i := 0; i < len(key); i++ {
		if key[i] < 0x20 || key[i] == 0x7f {
			return fmt.Errorf("%w: control character at index %d", ErrIdempotencyInvalidKey, i)
		}
	}
	return nil
}

// ComputeScopeHash computes the collision-free scope hash over the IAM-verified
// caller and operation tuple.
func ComputeScopeHash(s IdempotencyScope) string {
	return FrameHash(
		[]byte("scope:v1"),
		[]byte(s.Org),
		[]byte(s.Principal),
		[]byte(s.Project),
		[]byte(s.AuthScope),
		[]byte(s.Operation),
	)
}

// ComputeRequestFingerprint computes the length-prefixed SHA-256 fingerprint
// over (method, normalizedRoute, canonicalQuery, normalizedContentType, canonicalBody).
func ComputeRequestFingerprint(method, normalizedRoute string, query url.Values, contentType string, body []byte) string {
	var qbuf bytes.Buffer
	var lenBuf [8]byte
	writePart := func(b []byte) {
		binary.BigEndian.PutUint64(lenBuf[:], uint64(len(b)))
		_, _ = qbuf.Write(lenBuf[:])
		_, _ = qbuf.Write(b)
	}

	keys := make([]string, 0, len(query))
	for k := range query {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	binary.BigEndian.PutUint64(lenBuf[:], uint64(len(keys)))
	_, _ = qbuf.Write(lenBuf[:])
	for _, k := range keys {
		writePart([]byte(k))
		vals := append([]string(nil), query[k]...)
		sort.Strings(vals)
		binary.BigEndian.PutUint64(lenBuf[:], uint64(len(vals)))
		_, _ = qbuf.Write(lenBuf[:])
		for _, v := range vals {
			writePart([]byte(v))
		}
	}

	mediaType := normalizeMediaType(contentType)
	canonBody := body
	if isJSONBody(mediaType, body) {
		if c, ok := canonicalizeJSON(body); ok {
			canonBody = c
		}
	}

	return FrameHash(
		[]byte("req:v1"),
		[]byte(strings.ToUpper(strings.TrimSpace(method))),
		[]byte(normalizedRoute),
		qbuf.Bytes(),
		[]byte(mediaType),
		canonBody,
	)
}

func normalizeMediaType(ct string) string {
	ct = strings.TrimSpace(ct)
	if ct == "" {
		return ""
	}
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return strings.ToLower(ct)
	}
	return strings.ToLower(mt)
}

func isJSONBody(mediaType string, body []byte) bool {
	if mediaType == "application/json" || strings.HasSuffix(mediaType, "+json") {
		return true
	}
	trimmed := bytes.TrimSpace(body)
	return len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')
}

func canonicalizeJSON(raw []byte) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	// Ensure no trailing garbage after the first JSON value.
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, false
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	return out, true
}

// ── Optional KV accelerator (Valkey / Redis) ─────────────────────────────────

// IdempotencyKV is an optional acceleration layer for hot replay reads and
// cross-replica completion notifications. It is NEVER authoritative: eviction,
// restart, or unavailability MUST transparently fall back to SQL.
type IdempotencyKV interface {
	Get(ctx context.Context, cacheKey string) (*IdempotencyRecord, bool)
	Set(ctx context.Context, cacheKey string, rec *IdempotencyRecord, ttl time.Duration)
	Delete(ctx context.Context, cacheKey string)
	PublishDone(ctx context.Context, cacheKey string)
	SubscribeDone(ctx context.Context, cacheKey string) (<-chan struct{}, func())
}

const idemKVNotifyChannel = "ai:idem:done"

// RedisIdempotencyKV implements IdempotencyKV on top of Hanzo KV (Valkey/Redis).
type RedisIdempotencyKV struct {
	client *kv.Client
	prefix string
}

// NewRedisIdempotencyKV wraps a Valkey/Redis client as a non-authoritative
// idempotency accelerator.
func NewRedisIdempotencyKV(client *kv.Client) *RedisIdempotencyKV {
	if client == nil {
		return nil
	}
	return &RedisIdempotencyKV{
		client: client,
		prefix: "ai:idem:rec:",
	}
}

func (r *RedisIdempotencyKV) Get(ctx context.Context, cacheKey string) (*IdempotencyRecord, bool) {
	if r == nil || r.client == nil {
		return nil, false
	}
	cctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	data, err := r.client.Get(cctx, r.prefix+cacheKey).Bytes()
	if err != nil || len(data) == 0 {
		return nil, false
	}
	var rec IdempotencyRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, false
	}
	return &rec, true
}

func (r *RedisIdempotencyKV) Set(ctx context.Context, cacheKey string, rec *IdempotencyRecord, ttl time.Duration) {
	if r == nil || r.client == nil || rec == nil || ttl <= 0 {
		return
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	_ = r.client.Set(cctx, r.prefix+cacheKey, data, ttl).Err()
}

func (r *RedisIdempotencyKV) Delete(ctx context.Context, cacheKey string) {
	if r == nil || r.client == nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	_ = r.client.Del(cctx, r.prefix+cacheKey).Err()
}

func (r *RedisIdempotencyKV) PublishDone(ctx context.Context, cacheKey string) {
	if r == nil || r.client == nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	_ = r.client.Publish(cctx, idemKVNotifyChannel+":"+cacheKey, "1").Err()
}

func (r *RedisIdempotencyKV) SubscribeDone(ctx context.Context, cacheKey string) (<-chan struct{}, func()) {
	if r == nil || r.client == nil {
		return nil, func() {}
	}
	sub := r.client.Subscribe(ctx, idemKVNotifyChannel+":"+cacheKey)
	ch := make(chan struct{}, 1)
	done := make(chan struct{})
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			close(done)
			_ = sub.Close()
		})
	}
	go func() {
		defer close(ch)
		msgCh := sub.Channel()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case _, ok := <-msgCh:
				if !ok {
					return
				}
				select {
				case ch <- struct{}{}:
				default:
				}
				return
			}
		}
	}()
	return ch, cancel
}

// ── Bounded in-process L1 accelerator (strictly secondary to SQL) ────────────

type idemL1Entry struct {
	key  string
	rec  *IdempotencyRecord
	size int64
}

type idemL1Cache struct {
	mu       sync.Mutex
	maxBytes int64
	curBytes int64
	entries  map[string]*list.Element
	lru      *list.List
}

func newIdemL1Cache(maxBytes int64) *idemL1Cache {
	return &idemL1Cache{
		maxBytes: maxBytes,
		entries:  make(map[string]*list.Element),
		lru:      list.New(),
	}
}

func cloneRecord(r *IdempotencyRecord) *IdempotencyRecord {
	if r == nil {
		return nil
	}
	cp := *r
	if r.ResponseBody != nil {
		cp.ResponseBody = append([]byte(nil), r.ResponseBody...)
	}
	return &cp
}

func recordApproxSize(r *IdempotencyRecord) int64 {
	if r == nil {
		return 0
	}
	return int64(256 + len(r.ScopeHash) + len(r.IdempotencyKey) + len(r.ResponseHeaders) + len(r.ResponseBody) + len(r.ErrorMessage))
}

func (c *idemL1Cache) get(key string) (*IdempotencyRecord, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	c.lru.MoveToFront(el)
	return cloneRecord(el.Value.(*idemL1Entry).rec), true
}

func (c *idemL1Cache) set(key string, rec *IdempotencyRecord) {
	if rec == nil {
		return
	}
	cp := cloneRecord(rec)
	sz := recordApproxSize(cp)
	if sz > c.maxBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		old := el.Value.(*idemL1Entry)
		c.curBytes -= old.size
		old.rec = cp
		old.size = sz
		c.curBytes += sz
		c.lru.MoveToFront(el)
	} else {
		el = c.lru.PushFront(&idemL1Entry{key: key, rec: cp, size: sz})
		c.entries[key] = el
		c.curBytes += sz
	}
	for c.curBytes > c.maxBytes && c.lru.Len() > 0 {
		back := c.lru.Back()
		if back == nil {
			break
		}
		ev := back.Value.(*idemL1Entry)
		delete(c.entries, ev.key)
		c.curBytes -= ev.size
		c.lru.Remove(back)
	}
}

func (c *idemL1Cache) delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		c.curBytes -= el.Value.(*idemL1Entry).size
		delete(c.entries, key)
		c.lru.Remove(el)
	}
}

func (c *idemL1Cache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*list.Element)
	c.lru.Init()
	c.curBytes = 0
}

func (c *idemL1Cache) bytesUsed() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.curBytes
}

// ── Authoritative IdempotencyStore ───────────────────────────────────────────

// DBProvider resolves the authoritative database connection and driver name.
type DBProvider func() (db *dbx.DB, driverName string)

// IdempotencyStore manages durable idempotent execution records in SQL with
// optional KV/L1 acceleration and zero-polling waiter notifications.
type IdempotencyStore struct {
	dbProvider DBProvider
	cfg        IdempotencyConfig

	kvMu sync.RWMutex
	kv   IdempotencyKV

	l1 *idemL1Cache

	waitMu  sync.Mutex
	waiters map[string]map[chan struct{}]struct{}
}

// NewIdempotencyStore constructs a store backed by an explicit authoritative DB.
func NewIdempotencyStore(db *dbx.DB, driverName string, cfg IdempotencyConfig) *IdempotencyStore {
	return NewIdempotencyStoreWithProvider(func() (*dbx.DB, string) {
		return db, driverName
	}, cfg)
}

// NewIdempotencyStoreWithProvider constructs a store with a dynamic DB provider
// (e.g. reading object.adapter so UseMemoryDB/UseDB swaps apply seamlessly).
func NewIdempotencyStoreWithProvider(provider DBProvider, cfg IdempotencyConfig) *IdempotencyStore {
	cfg = cfg.normalized()
	return &IdempotencyStore{
		dbProvider: provider,
		cfg:        cfg,
		l1:         newIdemL1Cache(cfg.CacheMaxBytes),
		waiters:    make(map[string]map[chan struct{}]struct{}),
	}
}

// SetKV installs or clears the optional KV accelerator.
func (s *IdempotencyStore) SetKV(accel IdempotencyKV) {
	s.kvMu.Lock()
	s.kv = accel
	s.kvMu.Unlock()
}

func (s *IdempotencyStore) getKV() IdempotencyKV {
	s.kvMu.RLock()
	accel := s.kv
	s.kvMu.RUnlock()
	return accel
}

// Config returns the normalized store configuration.
func (s *IdempotencyStore) Config() IdempotencyConfig {
	return s.cfg
}

// EvictCaches clears the local L1 replay cache (used for testing cache eviction
// resilience against the authoritative SQL store).
func (s *IdempotencyStore) EvictCaches() {
	s.l1.clear()
}

// L1BytesUsed returns the current memory footprint of the bounded L1 cache.
func (s *IdempotencyStore) L1BytesUsed() int64 {
	return s.l1.bytesUsed()
}

func (s *IdempotencyStore) cacheKey(scopeHash, key string) string {
	return FrameHash([]byte("ck:v1"), []byte(scopeHash), []byte(key))
}

func (s *IdempotencyStore) db() (*dbx.DB, string, error) {
	if s == nil || s.dbProvider == nil {
		return nil, "", ErrIdempotencyStoreUnavailable
	}
	db, drv := s.dbProvider()
	if db == nil {
		return nil, "", ErrIdempotencyStoreUnavailable
	}
	return db, drv, nil
}

func (s *IdempotencyStore) wrapDBErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrIdempotencyStoreUnavailable) {
		return err
	}
	return fmt.Errorf("%w: %v", ErrIdempotencyStoreUnavailable, err)
}

func isSQLiteBusyErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "database table is locked") ||
		strings.Contains(msg, "sqlite_busy") ||
		strings.Contains(msg, "busy")
}

func retrySQLiteBusy(ctx context.Context, drv string, fn func() error) error {
	if drv != "sqlite" && drv != "sqlite3" && drv != "" {
		return fn()
	}
	var err error
	backoff := 2 * time.Millisecond
	for attempt := 0; attempt < 30; attempt++ {
		err = fn()
		if err == nil || !isSQLiteBusyErr(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 25*time.Millisecond {
			backoff *= 2
		}
	}
	return err
}

func (s *IdempotencyStore) notifyDone(ctx context.Context, scopeHash, key string) {
	ck := s.cacheKey(scopeHash, key)
	s.waitMu.Lock()
	set := s.waiters[ck]
	delete(s.waiters, ck)
	s.waitMu.Unlock()
	for ch := range set {
		close(ch)
	}
	if accel := s.getKV(); accel != nil {
		accel.PublishDone(ctx, ck)
	}
}

func (s *IdempotencyStore) registerWaiter(scopeHash, key string) (chan struct{}, func()) {
	ck := s.cacheKey(scopeHash, key)
	ch := make(chan struct{})
	s.waitMu.Lock()
	set := s.waiters[ck]
	if set == nil {
		set = make(map[chan struct{}]struct{})
		s.waiters[ck] = set
	}
	set[ch] = struct{}{}
	s.waitMu.Unlock()
	unreg := func() {
		s.waitMu.Lock()
		if set := s.waiters[ck]; set != nil {
			delete(set, ch)
			if len(set) == 0 {
				delete(s.waiters, ck)
			}
		}
		s.waitMu.Unlock()
	}
	return ch, unreg
}

func (s *IdempotencyStore) cacheTerminal(ctx context.Context, rec *IdempotencyRecord) {
	if rec == nil {
		return
	}
	if rec.State != IdemStateCompleted && rec.State != IdemStateFailed {
		return
	}
	ck := s.cacheKey(rec.ScopeHash, rec.IdempotencyKey)
	s.l1.set(ck, rec)
	if accel := s.getKV(); accel != nil {
		ttl := s.cfg.RecordTTL
		if exp, err := time.Parse(StampTime, rec.ExpiresAt); err == nil {
			if rem := exp.Sub(s.cfg.Now().UTC()); rem > 0 {
				ttl = rem
			} else {
				return
			}
		}
		accel.Set(ctx, ck, rec, ttl)
	}
}

func (s *IdempotencyStore) fetchRow(ctx context.Context, db *dbx.DB, drv, scopeHash, key string) (*IdempotencyRecord, bool, error) {
	var rec IdempotencyRecord
	var existed bool
	err := retrySQLiteBusy(ctx, drv, func() error {
		var qerr error
		existed, qerr = getOne(db, "idempotency_record", &rec, dbx.HashExp{
			"scope_hash":      scopeHash,
			"idempotency_key": key,
		})
		return qerr
	})
	if err != nil {
		return nil, false, s.wrapDBErr(err)
	}
	if !existed {
		return nil, false, nil
	}
	return &rec, true, nil
}

// Get reads the authoritative record directly from SQL.
func (s *IdempotencyStore) Get(ctx context.Context, scopeHash, key string) (*IdempotencyRecord, error) {
	db, drv, err := s.db()
	if err != nil {
		return nil, err
	}
	rec, ok, err := s.fetchRow(ctx, db, drv, scopeHash, key)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrIdempotencyNotFound
	}
	return rec, nil
}

// Claim atomically claims a new execution, replays an already-terminal execution,
// returns ClaimInFlight for an active lease, safely takes over an expired CLAIMED
// lease (preserving ExecutionID & BillingRef and incrementing FenceToken), or
// transitions an expired RUNNING lease to UNKNOWN (never silently re-executing).
func (s *IdempotencyStore) Claim(ctx context.Context, req IdempotencyClaimRequest) (*IdempotencyClaimResult, error) {
	if err := ValidateIdempotencyKey(req.IdempotencyKey, s.cfg.MaxKeyLength); err != nil {
		return nil, err
	}
	if req.Scope.Org == "" || req.Scope.Principal == "" || req.RequestHash == "" {
		return nil, fmt.Errorf("%w: missing verified scope or request fingerprint", ErrIdempotencyInvalidKey)
	}
	scopeHash := ComputeScopeHash(req.Scope)
	now := s.cfg.Now().UTC()
	nowStr := now.Format(StampTime)

	// Authoritative DB MUST be reachable (fail-closed on store outage).
	db, drv, err := s.db()
	if err != nil {
		return nil, err
	}

	// Fast hot-replay path from L1 or KV accelerator for immutable terminal records.
	ck := s.cacheKey(scopeHash, req.IdempotencyKey)
	if cached, ok := s.l1.get(ck); ok && cached.ExpiresAt > nowStr {
		if res, handled, cerr := s.terminalFromCache(cached, req.RequestHash); handled {
			return res, cerr
		}
	}
	if accel := s.getKV(); accel != nil {
		if cached, ok := accel.Get(ctx, ck); ok && cached.ExpiresAt > nowStr {
			if res, handled, cerr := s.terminalFromCache(cached, req.RequestHash); handled {
				s.l1.set(ck, cached)
				return res, cerr
			}
		}
	}

	// Authoritative SQL claim/transition loop.
	for attempt := 0; attempt < 8; attempt++ {
		now = s.cfg.Now().UTC()
		nowStr = now.Format(StampTime)
		leaseStr := now.Add(s.cfg.LeaseTTL).UTC().Format(StampTime)
		expiresStr := now.Add(s.cfg.RecordTTL).UTC().Format(StampTime)

		rec, exists, err := s.fetchRow(ctx, db, drv, scopeHash, req.IdempotencyKey)
		if err != nil {
			return nil, err
		}

		if !exists {
			execID := uuid.NewString()
			billingRef := "exec:" + execID
			newRec := &IdempotencyRecord{
				ScopeHash:            scopeHash,
				IdempotencyKey:       req.IdempotencyKey,
				Org:                  req.Scope.Org,
				Principal:            req.Scope.Principal,
				Project:              req.Scope.Project,
				AuthScope:            req.Scope.AuthScope,
				Operation:            req.Scope.Operation,
				RequestHash:          req.RequestHash,
				ExecutionID:          execID,
				BillingRef:           billingRef,
				State:                IdemStateClaimed,
				FenceToken:           1,
				OwnerID:              req.OwnerID,
				LeaseUntil:           leaseStr,
				SideEffectsStarted:   false,
				SideEffectsCommitted: false,
				Streaming:            req.Streaming,
				StreamDisconnected:   false,
				ResponseRetained:     false,
				CreatedAt:            nowStr,
				UpdatedAt:            nowStr,
				ExpiresAt:            expiresStr,
			}

			inserted, err := s.insertClaim(ctx, db, drv, newRec, nowStr)
			if err != nil {
				return nil, err
			}
			if inserted {
				return &IdempotencyClaimResult{
					Outcome: ClaimOwned,
					Record:  newRec,
				}, nil
			}
			// Either a concurrent replica inserted the same key first OR the
			// per-tenant cardinality guard blocked the insert. Disambiguate cleanly.
			if s.cfg.MaxKeysPerTenant > 0 {
				if _, nowExists, ferr := s.fetchRow(ctx, db, drv, scopeHash, req.IdempotencyKey); ferr != nil {
					return nil, ferr
				} else if !nowExists {
					return nil, ErrIdempotencyCardinalityExceeded
				}
			}
			// Concurrent replica inserted first; loop to inspect the authoritative row.
			continue
		}

		// Recycle expired terminal records so the key can be reused after RecordTTL.
		if rec.ExpiresAt != "" && rec.ExpiresAt <= nowStr &&
			(rec.State == IdemStateCompleted || rec.State == IdemStateFailed) {
			var res sql.Result
			err = retrySQLiteBusy(ctx, drv, func() error {
				var qerr error
				res, qerr = db.NewQuery(
					"DELETE FROM {{idempotency_record}} WHERE [[scope_hash]] = {:scope} AND [[idempotency_key]] = {:key} AND [[fence_token]] = {:fence} AND [[expires_at]] <= {:now} AND [[state]] IN ('COMPLETED', 'FAILED')",
				).Bind(dbx.Params{
					"scope": scopeHash,
					"key":   req.IdempotencyKey,
					"fence": rec.FenceToken,
					"now":   nowStr,
				}).Execute()
				return qerr
			})
			if err != nil {
				return nil, s.wrapDBErr(err)
			}
			if res != nil {
				if n, _ := res.RowsAffected(); n > 0 {
					s.l1.delete(ck)
					if accel := s.getKV(); accel != nil {
						accel.Delete(ctx, ck)
					}
				}
			}
			continue
		}

		// Fingerprint mismatch is ALWAYS a 409 Conflict regardless of state.
		if rec.RequestHash != req.RequestHash {
			return nil, ErrIdempotencyConflict
		}

		switch rec.State {
		case IdemStateCompleted:
			s.cacheTerminal(ctx, rec)
			return &IdempotencyClaimResult{Outcome: ClaimCompleted, Record: rec}, nil

		case IdemStateFailed:
			s.cacheTerminal(ctx, rec)
			return &IdempotencyClaimResult{Outcome: ClaimFailed, Record: rec}, nil

		case IdemStateUnknown:
			return &IdempotencyClaimResult{Outcome: ClaimUnknown, Record: rec}, nil

		case IdemStateClaimed:
			if rec.LeaseUntil > nowStr {
				return &IdempotencyClaimResult{Outcome: ClaimInFlight, Record: rec}, nil
			}
			// Expired CLAIMED lease: safe takeover ONLY if side effects have NOT started.
			if !rec.SideEffectsStarted {
				var res sql.Result
				err = retrySQLiteBusy(ctx, drv, func() error {
					var qerr error
					res, qerr = db.NewQuery(
						"UPDATE {{idempotency_record}} SET [[owner_id]] = {:owner}, [[fence_token]] = [[fence_token]] + 1, [[lease_until]] = {:lease}, [[updated_at]] = {:now} "+
							"WHERE [[scope_hash]] = {:scope} AND [[idempotency_key]] = {:key} AND [[state]] = 'CLAIMED' AND [[side_effects_started]] = {:notStarted} AND [[fence_token]] = {:oldFence} AND [[lease_until]] <= {:now}",
					).Bind(dbx.Params{
						"owner":      req.OwnerID,
						"lease":      leaseStr,
						"now":        nowStr,
						"scope":      scopeHash,
						"key":        req.IdempotencyKey,
						"notStarted": false,
						"oldFence":   rec.FenceToken,
					}).Execute()
					return qerr
				})
				if err != nil {
					return nil, s.wrapDBErr(err)
				}
				if n, _ := res.RowsAffected(); n == 1 {
					rec.OwnerID = req.OwnerID
					rec.FenceToken++
					rec.LeaseUntil = leaseStr
					rec.UpdatedAt = nowStr
					return &IdempotencyClaimResult{Outcome: ClaimOwned, Record: rec}, nil
				}
				continue
			}
			// Side effects somehow started while state was CLAIMED: treat as RUNNING expiry -> UNKNOWN.
			fallthrough

		case IdemStateRunning:
			if rec.LeaseUntil > nowStr {
				return &IdempotencyClaimResult{Outcome: ClaimInFlight, Record: rec}, nil
			}
			// Expired RUNNING lease: worker crashed after side effects started.
			// Transition atomically to UNKNOWN and bump fence_token so the stale
			// worker is permanently fenced out and no blind retry can double-execute.
			var res sql.Result
			err = retrySQLiteBusy(ctx, drv, func() error {
				var qerr error
				res, qerr = db.NewQuery(
					"UPDATE {{idempotency_record}} SET [[state]] = 'UNKNOWN', [[fence_token]] = [[fence_token]] + 1, [[error_code]] = {:ecode}, [[error_message]] = {:emsg}, [[updated_at]] = {:now} "+
						"WHERE [[scope_hash]] = {:scope} AND [[idempotency_key]] = {:key} AND [[state]] IN ('CLAIMED', 'RUNNING') AND [[fence_token]] = {:oldFence} AND [[lease_until]] <= {:now}",
				).Bind(dbx.Params{
					"ecode":    "execution_outcome_unknown",
					"emsg":     "worker lease expired while execution was running; reconciliation required",
					"now":      nowStr,
					"scope":    scopeHash,
					"key":      req.IdempotencyKey,
					"oldFence": rec.FenceToken,
				}).Execute()
				return qerr
			})
			if err != nil {
				return nil, s.wrapDBErr(err)
			}
			if n, _ := res.RowsAffected(); n == 1 {
				rec.State = IdemStateUnknown
				rec.FenceToken++
				rec.ErrorCode = "execution_outcome_unknown"
				rec.ErrorMessage = "worker lease expired while execution was running; reconciliation required"
				rec.UpdatedAt = nowStr
				s.notifyDone(ctx, scopeHash, req.IdempotencyKey)
				return &IdempotencyClaimResult{Outcome: ClaimUnknown, Record: rec}, nil
			}
			continue
		}
	}

	// Fallback read if extreme contention exhausted retries.
	rec, exists, err := s.fetchRow(ctx, db, drv, scopeHash, req.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrIdempotencyStoreUnavailable
	}
	if rec.RequestHash != req.RequestHash {
		return nil, ErrIdempotencyConflict
	}
	switch rec.State {
	case IdemStateCompleted:
		return &IdempotencyClaimResult{Outcome: ClaimCompleted, Record: rec}, nil
	case IdemStateFailed:
		return &IdempotencyClaimResult{Outcome: ClaimFailed, Record: rec}, nil
	case IdemStateUnknown:
		return &IdempotencyClaimResult{Outcome: ClaimUnknown, Record: rec}, nil
	default:
		return &IdempotencyClaimResult{Outcome: ClaimInFlight, Record: rec}, nil
	}
}

func (s *IdempotencyStore) terminalFromCache(cached *IdempotencyRecord, reqHash string) (*IdempotencyClaimResult, bool, error) {
	if cached.State != IdemStateCompleted && cached.State != IdemStateFailed {
		return nil, false, nil
	}
	if cached.RequestHash != reqHash {
		return nil, true, ErrIdempotencyConflict
	}
	if cached.State == IdemStateCompleted {
		return &IdempotencyClaimResult{Outcome: ClaimCompleted, Record: cached}, true, nil
	}
	return &IdempotencyClaimResult{Outcome: ClaimFailed, Record: cached}, true, nil
}

func (s *IdempotencyStore) insertClaim(ctx context.Context, db *dbx.DB, drv string, rec *IdempotencyRecord, nowStr string) (bool, error) {
	cols := "[[scope_hash]], [[idempotency_key]], [[org]], [[principal]], [[project]], [[auth_scope]], [[operation]], " +
		"[[request_hash]], [[execution_id]], [[billing_ref]], [[state]], [[fence_token]], [[owner_id]], [[lease_until]], " +
		"[[side_effects_started]], [[side_effects_committed]], [[streaming]], [[stream_disconnected]], " +
		"[[response_status]], [[response_headers]], [[response_body]], [[response_retained]], [[non_replay_reason]], " +
		"[[error_code]], [[error_message]], [[created_at]], [[updated_at]], [[expires_at]]"
	vals := "{:scope_hash}, {:idempotency_key}, {:org}, {:principal}, {:project}, {:auth_scope}, {:operation}, " +
		"{:request_hash}, {:execution_id}, {:billing_ref}, {:state}, {:fence_token}, {:owner_id}, {:lease_until}, " +
		"{:side_effects_started}, {:side_effects_committed}, {:streaming}, {:stream_disconnected}, " +
		"{:response_status}, {:response_headers}, {:response_body}, {:response_retained}, {:non_replay_reason}, " +
		"{:error_code}, {:error_message}, {:created_at}, {:updated_at}, {:expires_at}"

	var stmt string
	if s.cfg.MaxKeysPerTenant > 0 {
		// Single atomic SQL statement enforces per-tenant active cardinality
		// without a check-then-insert TOCTOU race across concurrent replicas.
		if drv == "mysql" {
			stmt = "INSERT IGNORE INTO {{idempotency_record}} (" + cols + ") SELECT " + vals +
				" WHERE (SELECT COUNT(*) FROM {{idempotency_record}} WHERE [[org]] = {:org} AND [[expires_at]] > {:now}) < {:max_keys}"
		} else {
			stmt = "INSERT INTO {{idempotency_record}} (" + cols + ") SELECT " + vals +
				" WHERE (SELECT COUNT(*) FROM {{idempotency_record}} WHERE [[org]] = {:org} AND [[expires_at]] > {:now}) < {:max_keys}" +
				" ON CONFLICT ([[scope_hash]], [[idempotency_key]]) DO NOTHING"
		}
	} else {
		if drv == "mysql" {
			stmt = "INSERT IGNORE INTO {{idempotency_record}} (" + cols + ") VALUES (" + vals + ")"
		} else {
			stmt = "INSERT INTO {{idempotency_record}} (" + cols + ") VALUES (" + vals + ") ON CONFLICT ([[scope_hash]], [[idempotency_key]]) DO NOTHING"
		}
	}
	params := dbx.Params{
		"scope_hash":             rec.ScopeHash,
		"idempotency_key":        rec.IdempotencyKey,
		"org":                    rec.Org,
		"principal":              rec.Principal,
		"project":                rec.Project,
		"auth_scope":             rec.AuthScope,
		"operation":              rec.Operation,
		"request_hash":           rec.RequestHash,
		"execution_id":           rec.ExecutionID,
		"billing_ref":            rec.BillingRef,
		"state":                  rec.State,
		"fence_token":            rec.FenceToken,
		"owner_id":               rec.OwnerID,
		"lease_until":            rec.LeaseUntil,
		"side_effects_started":   rec.SideEffectsStarted,
		"side_effects_committed": rec.SideEffectsCommitted,
		"streaming":              rec.Streaming,
		"stream_disconnected":    rec.StreamDisconnected,
		"response_status":        rec.ResponseStatus,
		"response_headers":       rec.ResponseHeaders,
		"response_body":          rec.ResponseBody,
		"response_retained":      rec.ResponseRetained,
		"non_replay_reason":      rec.NonReplayReason,
		"error_code":             rec.ErrorCode,
		"error_message":          rec.ErrorMessage,
		"created_at":             rec.CreatedAt,
		"updated_at":             rec.UpdatedAt,
		"expires_at":             rec.ExpiresAt,
		"now":                    nowStr,
		"max_keys":               s.cfg.MaxKeysPerTenant,
	}

	var res sql.Result
	err := retrySQLiteBusy(ctx, drv, func() error {
		var qerr error
		res, qerr = db.NewQuery(stmt).Bind(params).Execute()
		return qerr
	})
	if err != nil {
		return false, s.wrapDBErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, s.wrapDBErr(err)
	}
	return n == 1, nil
}

// MarkRunning atomically transitions CLAIMED -> RUNNING and sets side_effects_started=true,
// fenced by fenceToken. Returns ErrIdempotencyFenced if another worker took over.
func (s *IdempotencyStore) MarkRunning(ctx context.Context, scopeHash, key string, fenceToken int64) error {
	db, drv, err := s.db()
	if err != nil {
		return err
	}
	nowStr := s.cfg.Now().UTC().Format(StampTime)
	var res sql.Result
	err = retrySQLiteBusy(ctx, drv, func() error {
		var qerr error
		res, qerr = db.NewQuery(
			"UPDATE {{idempotency_record}} SET [[state]] = 'RUNNING', [[side_effects_started]] = {:started}, [[updated_at]] = {:now} "+
				"WHERE [[scope_hash]] = {:scope} AND [[idempotency_key]] = {:key} AND [[state]] = 'CLAIMED' AND [[fence_token]] = {:fence}",
		).Bind(dbx.Params{
			"started": true,
			"now":     nowStr,
			"scope":   scopeHash,
			"key":     key,
			"fence":   fenceToken,
		}).Execute()
		return qerr
	})
	if err != nil {
		return s.wrapDBErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return s.wrapDBErr(err)
	}
	if n == 0 {
		return ErrIdempotencyFenced
	}
	return nil
}

// RenewLease extends lease_until for an active CLAIMED or RUNNING execution,
// fenced by fenceToken.
func (s *IdempotencyStore) RenewLease(ctx context.Context, scopeHash, key string, fenceToken int64) error {
	db, drv, err := s.db()
	if err != nil {
		return err
	}
	now := s.cfg.Now().UTC()
	nowStr := now.Format(StampTime)
	leaseStr := now.Add(s.cfg.LeaseTTL).UTC().Format(StampTime)
	var res sql.Result
	err = retrySQLiteBusy(ctx, drv, func() error {
		var qerr error
		res, qerr = db.NewQuery(
			"UPDATE {{idempotency_record}} SET [[lease_until]] = {:lease}, [[updated_at]] = {:now} "+
				"WHERE [[scope_hash]] = {:scope} AND [[idempotency_key]] = {:key} AND [[state]] IN ('CLAIMED', 'RUNNING') AND [[fence_token]] = {:fence}",
		).Bind(dbx.Params{
			"lease": leaseStr,
			"now":   nowStr,
			"scope": scopeHash,
			"key":   key,
			"fence": fenceToken,
		}).Execute()
		return qerr
	})
	if err != nil {
		return s.wrapDBErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return s.wrapDBErr(err)
	}
	if n == 0 {
		return ErrIdempotencyFenced
	}
	return nil
}

// MarkSideEffectsCommitted records that external mutations / billing debits
// landed for this execution, fenced by fenceToken.
func (s *IdempotencyStore) MarkSideEffectsCommitted(ctx context.Context, scopeHash, key string, fenceToken int64) error {
	db, drv, err := s.db()
	if err != nil {
		return err
	}
	nowStr := s.cfg.Now().UTC().Format(StampTime)
	var res sql.Result
	err = retrySQLiteBusy(ctx, drv, func() error {
		var qerr error
		res, qerr = db.NewQuery(
			"UPDATE {{idempotency_record}} SET [[side_effects_committed]] = {:committed}, [[updated_at]] = {:now} "+
				"WHERE [[scope_hash]] = {:scope} AND [[idempotency_key]] = {:key} AND [[fence_token]] = {:fence}",
		).Bind(dbx.Params{
			"committed": true,
			"now":       nowStr,
			"scope":     scopeHash,
			"key":       key,
			"fence":     fenceToken,
		}).Execute()
		return qerr
	})
	if err != nil {
		return s.wrapDBErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return s.wrapDBErr(err)
	}
	if n == 0 {
		return ErrIdempotencyFenced
	}
	return nil
}

// MarkStreamDisconnected records a client mid-stream disconnect while preserving
// the durable execution & billing IDs, fenced by fenceToken.
func (s *IdempotencyStore) MarkStreamDisconnected(ctx context.Context, scopeHash, key string, fenceToken int64) error {
	db, drv, err := s.db()
	if err != nil {
		return err
	}
	nowStr := s.cfg.Now().UTC().Format(StampTime)
	var res sql.Result
	err = retrySQLiteBusy(ctx, drv, func() error {
		var qerr error
		res, qerr = db.NewQuery(
			"UPDATE {{idempotency_record}} SET [[streaming]] = {:streaming}, [[stream_disconnected]] = {:disc}, [[response_retained]] = {:ret}, [[non_replay_reason]] = {:reason}, [[updated_at]] = {:now} "+
				"WHERE [[scope_hash]] = {:scope} AND [[idempotency_key]] = {:key} AND [[fence_token]] = {:fence}",
		).Bind(dbx.Params{
			"streaming": true,
			"disc":      true,
			"ret":       false,
			"reason":    IdemNonReplayStreamDisconnected,
			"now":       nowStr,
			"scope":     scopeHash,
			"key":       key,
			"fence":     fenceToken,
		}).Execute()
		return qerr
	})
	if err != nil {
		return s.wrapDBErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return s.wrapDBErr(err)
	}
	if n == 0 {
		return ErrIdempotencyFenced
	}
	// Refresh cache if terminal.
	if rec, ok, gerr := s.fetchRow(ctx, db, drv, scopeHash, key); gerr == nil && ok {
		s.cacheTerminal(ctx, rec)
	}
	return nil
}

// Complete commits a terminal COMPLETED outcome, fenced by fenceToken.
// Oversized or streaming responses are persisted with ResponseRetained=false and
// ResponseBody=nil so retries NEVER re-execute the completed mutation.
func (s *IdempotencyStore) Complete(ctx context.Context, scopeHash, key string, fenceToken int64, outcome IdempotencyOutcome) error {
	return s.finalize(ctx, scopeHash, key, fenceToken, IdemStateCompleted, outcome)
}

// Fail commits a terminal FAILED outcome, fenced by fenceToken.
func (s *IdempotencyStore) Fail(ctx context.Context, scopeHash, key string, fenceToken int64, outcome IdempotencyOutcome) error {
	return s.finalize(ctx, scopeHash, key, fenceToken, IdemStateFailed, outcome)
}

// MarkUnknown transitions a CLAIMED or RUNNING execution to UNKNOWN after an
// ambiguous timeout, panic, or disconnect, fenced by fenceToken.
func (s *IdempotencyStore) MarkUnknown(ctx context.Context, scopeHash, key string, fenceToken int64, code, message string, sideEffectsCommitted bool) error {
	db, drv, err := s.db()
	if err != nil {
		return err
	}
	if code == "" {
		code = "execution_outcome_unknown"
	}
	nowStr := s.cfg.Now().UTC().Format(StampTime)
	var res sql.Result
	err = retrySQLiteBusy(ctx, drv, func() error {
		var qerr error
		res, qerr = db.NewQuery(
			"UPDATE {{idempotency_record}} SET [[state]] = 'UNKNOWN', [[error_code]] = {:ecode}, [[error_message]] = {:emsg}, [[side_effects_committed]] = CASE WHEN [[side_effects_committed]] = {:t} THEN {:t} ELSE {:comm} END, [[updated_at]] = {:now} "+
				"WHERE [[scope_hash]] = {:scope} AND [[idempotency_key]] = {:key} AND [[state]] IN ('CLAIMED', 'RUNNING') AND [[fence_token]] = {:fence}",
		).Bind(dbx.Params{
			"ecode": code,
			"emsg":  truncateString(message, 1024),
			"t":     true,
			"comm":  sideEffectsCommitted,
			"now":   nowStr,
			"scope": scopeHash,
			"key":   key,
			"fence": fenceToken,
		}).Execute()
		return qerr
	})
	if err != nil {
		return s.wrapDBErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return s.wrapDBErr(err)
	}
	if n == 0 {
		return ErrIdempotencyFenced
	}
	s.notifyDone(ctx, scopeHash, key)
	return nil
}

// Reconcile resolves an UNKNOWN record into a terminal COMPLETED or FAILED
// outcome after external reconciliation, bumping fence_token atomically.
func (s *IdempotencyStore) Reconcile(ctx context.Context, scopeHash, key string, expectedFenceToken int64, targetState string, outcome IdempotencyOutcome) error {
	if targetState != IdemStateCompleted && targetState != IdemStateFailed {
		return fmt.Errorf("%w: reconcile target must be COMPLETED or FAILED, got %q", ErrIdempotencyInvalidTransition, targetState)
	}
	db, drv, err := s.db()
	if err != nil {
		return err
	}
	retained, body, nonReplayReason := s.boundResponse(outcome)
	headersJSON := encodeBoundedHeaders(outcome.Headers, s.cfg.MaxHeaderBytes)
	nowStr := s.cfg.Now().UTC().Format(StampTime)

	var res sql.Result
	err = retrySQLiteBusy(ctx, drv, func() error {
		var qerr error
		res, qerr = db.NewQuery(
			"UPDATE {{idempotency_record}} SET [[state]] = {:state}, [[fence_token]] = [[fence_token]] + 1, "+
				"[[response_status]] = {:status}, [[response_headers]] = {:headers}, [[response_body]] = {:body}, "+
				"[[response_retained]] = {:retained}, [[non_replay_reason]] = {:reason}, "+
				"[[streaming]] = {:streaming}, [[stream_disconnected]] = {:disc}, "+
				"[[side_effects_committed]] = CASE WHEN [[side_effects_committed]] = {:t} THEN {:t} ELSE {:comm} END, "+
				"[[error_code]] = {:ecode}, [[error_message]] = {:emsg}, [[updated_at]] = {:now} "+
				"WHERE [[scope_hash]] = {:scope} AND [[idempotency_key]] = {:key} AND [[state]] = 'UNKNOWN' AND [[fence_token]] = {:fence}",
		).Bind(dbx.Params{
			"state":     targetState,
			"status":    outcome.Status,
			"headers":   headersJSON,
			"body":      body,
			"retained":  retained,
			"reason":    nonReplayReason,
			"streaming": outcome.Streaming,
			"disc":      outcome.StreamDisconnected,
			"t":         true,
			"comm":      outcome.SideEffectsCommitted,
			"ecode":     truncateString(outcome.ErrorCode, 128),
			"emsg":      truncateString(outcome.ErrorMessage, 1024),
			"now":       nowStr,
			"scope":     scopeHash,
			"key":       key,
			"fence":     expectedFenceToken,
		}).Execute()
		return qerr
	})
	if err != nil {
		return s.wrapDBErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return s.wrapDBErr(err)
	}
	if n == 0 {
		return ErrIdempotencyFenced
	}
	if rec, ok, gerr := s.fetchRow(ctx, db, drv, scopeHash, key); gerr == nil && ok {
		s.cacheTerminal(ctx, rec)
	}
	s.notifyDone(ctx, scopeHash, key)
	return nil
}

func (s *IdempotencyStore) finalize(ctx context.Context, scopeHash, key string, fenceToken int64, targetState string, outcome IdempotencyOutcome) error {
	db, drv, err := s.db()
	if err != nil {
		return err
	}
	retained, body, nonReplayReason := s.boundResponse(outcome)
	headersJSON := encodeBoundedHeaders(outcome.Headers, s.cfg.MaxHeaderBytes)
	nowStr := s.cfg.Now().UTC().Format(StampTime)

	var res sql.Result
	err = retrySQLiteBusy(ctx, drv, func() error {
		var qerr error
		res, qerr = db.NewQuery(
			"UPDATE {{idempotency_record}} SET [[state]] = {:state}, "+
				"[[response_status]] = {:status}, [[response_headers]] = {:headers}, [[response_body]] = {:body}, "+
				"[[response_retained]] = {:retained}, [[non_replay_reason]] = {:reason}, "+
				"[[streaming]] = {:streaming}, [[stream_disconnected]] = CASE WHEN [[stream_disconnected]] = {:t} THEN {:t} ELSE {:disc} END, "+
				"[[side_effects_committed]] = CASE WHEN [[side_effects_committed]] = {:t} THEN {:t} ELSE {:comm} END, "+
				"[[error_code]] = {:ecode}, [[error_message]] = {:emsg}, [[updated_at]] = {:now} "+
				"WHERE [[scope_hash]] = {:scope} AND [[idempotency_key]] = {:key} AND [[state]] IN ('CLAIMED', 'RUNNING') AND [[fence_token]] = {:fence}",
		).Bind(dbx.Params{
			"state":     targetState,
			"status":    outcome.Status,
			"headers":   headersJSON,
			"body":      body,
			"retained":  retained,
			"reason":    nonReplayReason,
			"streaming": outcome.Streaming,
			"disc":      outcome.StreamDisconnected,
			"t":         true,
			"comm":      outcome.SideEffectsCommitted,
			"ecode":     truncateString(outcome.ErrorCode, 128),
			"emsg":      truncateString(outcome.ErrorMessage, 1024),
			"now":       nowStr,
			"scope":     scopeHash,
			"key":       key,
			"fence":     fenceToken,
		}).Execute()
		return qerr
	})
	if err != nil {
		return s.wrapDBErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return s.wrapDBErr(err)
	}
	if n == 0 {
		return ErrIdempotencyFenced
	}
	if rec, ok, gerr := s.fetchRow(ctx, db, drv, scopeHash, key); gerr == nil && ok {
		s.cacheTerminal(ctx, rec)
	}
	s.notifyDone(ctx, scopeHash, key)
	return nil
}

func (s *IdempotencyStore) boundResponse(o IdempotencyOutcome) (retained bool, body []byte, reason string) {
	if o.Streaming {
		if o.StreamDisconnected {
			return false, nil, IdemNonReplayStreamDisconnected
		}
		return false, nil, IdemNonReplayStreaming
	}
	if len(o.Body) > s.cfg.MaxResponseBytes {
		return false, nil, IdemNonReplayOversized
	}
	if o.Body != nil {
		body = append([]byte(nil), o.Body...)
	}
	return true, body, ""
}

// WaitForCompletion waits up to timeout for an in-flight execution to reach a
// terminal (COMPLETED / FAILED / UNKNOWN) state or lose its lease.
//
// Uses synchronized channel notifications (in-process) and KV pub/sub
// (cross-replica) with a fallback timer when KV is absent — zero unconditional
// polling sleep on the hot path.
func (s *IdempotencyStore) WaitForCompletion(ctx context.Context, scopeHash, key string, timeout time.Duration) (*IdempotencyRecord, bool, error) {
	if timeout <= 0 {
		rec, err := s.Get(ctx, scopeHash, key)
		if err != nil {
			return nil, false, err
		}
		return rec, isTerminalOrUnknown(rec.State), nil
	}

	localCh, unreg := s.registerWaiter(scopeHash, key)
	defer unreg()

	var kvCh <-chan struct{}
	var kvCancel func()
	if accel := s.getKV(); accel != nil {
		kvCh, kvCancel = accel.SubscribeDone(ctx, s.cacheKey(scopeHash, key))
		defer kvCancel()
	}

	// Re-check immediately after registering waiters so a completion that raced
	// with registration is never missed.
	check := func() (*IdempotencyRecord, bool, error) {
		rec, err := s.Get(ctx, scopeHash, key)
		if err != nil {
			return nil, false, err
		}
		if isTerminalOrUnknown(rec.State) {
			return rec, true, nil
		}
		return rec, false, nil
	}

	rec, done, err := check()
	if err != nil || done {
		return rec, done, err
	}

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	// Cross-replica fallback check when no KV pub/sub is attached: starts fast
	// and backs off exponentially up to 100ms, never blocking local/KV wakeups.
	var recheck *time.Timer
	var recheckC <-chan time.Time
	recheckInterval := 15 * time.Millisecond
	if kvCh == nil {
		recheck = time.NewTimer(recheckInterval)
		defer recheck.Stop()
		recheckC = recheck.C
	}

	for {
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-deadline.C:
			return check()
		case <-localCh:
			return check()
		case _, ok := <-kvCh:
			if !ok {
				kvCh = nil
				if recheck == nil {
					recheck = time.NewTimer(recheckInterval)
					defer recheck.Stop()
					recheckC = recheck.C
				}
				continue
			}
			return check()
		case <-recheckC:
			rec, done, err = check()
			if err != nil || done {
				return rec, done, err
			}
			// If lease already expired, stop waiting so caller can attempt takeover/UNKNOWN transition.
			if rec != nil && rec.LeaseUntil <= s.cfg.Now().UTC().Format(StampTime) {
				return rec, false, nil
			}
			if recheckInterval < 100*time.Millisecond {
				recheckInterval *= 2
				if recheckInterval > 100*time.Millisecond {
					recheckInterval = 100 * time.Millisecond
				}
			}
			recheck.Reset(recheckInterval)
		}
	}
}

func isTerminalOrUnknown(state string) bool {
	switch state {
	case IdemStateCompleted, IdemStateFailed, IdemStateUnknown:
		return true
	}
	return false
}

// DecodeResponseHeaders parses bounded JSON response headers stored on a record.
func DecodeResponseHeaders(raw string) map[string]string {
	if raw == "" {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}
	return m
}

func encodeBoundedHeaders(headers map[string]string, maxBytes int) string {
	if len(headers) == 0 || maxBytes <= 0 {
		return ""
	}
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	bounded := make(map[string]string, len(keys))
	used := 2 // "{}"
	for _, k := range keys {
		v := headers[k]
		if v == "" {
			continue
		}
		// Conservative JSON entry size estimate.
		entrySize := len(k) + len(v) + 8
		if used+entrySize > maxBytes {
			// Truncate value if there's room for at least key + small value, else stop.
			rem := maxBytes - used - len(k) - 8
			if rem <= 0 {
				break
			}
			v = v[:rem]
			bounded[k] = v
			break
		}
		bounded[k] = v
		used += entrySize
	}
	if len(bounded) == 0 {
		return ""
	}
	b, err := json.Marshal(bounded)
	if err != nil || len(b) > maxBytes {
		return ""
	}
	return string(b)
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

// ── Package default store & optional KV wiring ───────────────────────────────

var (
	defaultIdemStoreOnce sync.Once
	defaultIdemStore     *IdempotencyStore
)

// DefaultIdempotencyStore returns the process-wide authoritative idempotency
// store backed by object.adapter (SQLite or PostgreSQL).
func DefaultIdempotencyStore() *IdempotencyStore {
	defaultIdemStoreOnce.Do(func() {
		defaultIdemStore = NewIdempotencyStoreWithProvider(func() (*dbx.DB, string) {
			if adapter == nil || adapter.db == nil {
				return nil, ""
			}
			return adapter.db, adapter.driverName
		}, DefaultIdempotencyConfig())
	})
	return defaultIdemStore
}

// InitIdempotencyKV attaches Valkey/Redis acceleration to DefaultIdempotencyStore
// when KV_URL is configured. No-op when unset: SQL remains authoritative.
func InitIdempotencyKV() {
	rawURL := strings.TrimSpace(os.Getenv("KV_URL"))
	if rawURL == "" {
		return
	}
	opts, err := kv.ParseURL(rawURL)
	if err != nil {
		log.Error("idempotency kv: invalid KV_URL, staying SQL-only: %v", err)
		return
	}
	client := kv.NewClient(opts)
	DefaultIdempotencyStore().SetKV(NewRedisIdempotencyKV(client))
	log.Info("idempotency kv: enabled acceleration over KV_URL (SQL remains authoritative)")
}
