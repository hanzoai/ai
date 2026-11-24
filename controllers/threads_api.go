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

package controllers

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/ai/object"
)

// ── Thread Store REST API ─────────────────────────────────────────────────────

// requireThreadCaller authenticates the request from a verified credential
// (session cookie, signed IAM JWT, IAM API key, run key, or provider API key)
// and returns (callerOrg, callerUser, true). Never trusts raw client headers.
func (c *ApiController) requireThreadCaller() (string, string, bool) {
	if user := c.principalUser(); user != nil {
		org := strings.TrimSpace(c.GetOrg())
		if org == "" {
			// A verified person whose signed membership set opens no org is denied
			// rather than silently falling back to a brand org.
			c.ResponseErrorWithStatus(http.StatusUnauthorized, "auth:no active organization for caller")
			return "", "", false
		}
		return org, user.Name, true
	}
	token := c.presented()
	if token != "" && !isPublishableKey(token) && !isJwtToken(token) {
		if prov, err := object.GetProviderByProviderKey(token, c.GetAcceptLanguage()); err == nil && prov != nil && strings.TrimSpace(prov.Owner) != "" {
			return strings.TrimSpace(prov.Owner), "provider:" + prov.Name, true
		}
	}
	c.ResponseErrorWithStatus(http.StatusUnauthorized, "auth:authentication required")
	return "", "", false
}

func (c *ApiController) writeJSON(status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		c.ResponseErrorWithStatus(http.StatusInternalServerError, err.Error())
		return
	}
	c.SetHeader("Content-Type", "application/json")
	_ = c.Bytes(status, b)
}

func (c *ApiController) threadStoreErr(err error) {
	switch {
	case errors.Is(err, object.ErrThreadNotFound), errors.Is(err, object.ErrBranchNotFound), errors.Is(err, object.ErrChunkNotFound):
		c.ResponseErrorWithStatus(http.StatusNotFound, err.Error())
	case errors.Is(err, object.ErrLeaseConflict), errors.Is(err, object.ErrBranchExists):
		c.ResponseErrorWithStatus(http.StatusConflict, err.Error())
	case errors.Is(err, object.ErrForbidden):
		c.ResponseErrorWithStatus(http.StatusForbidden, err.Error())
	case errors.Is(err, object.ErrInvalidGrant), errors.Is(err, object.ErrInvalidArgument):
		c.ResponseErrorWithStatus(http.StatusBadRequest, err.Error())
	default:
		c.ResponseErrorWithStatus(http.StatusInternalServerError, err.Error())
	}
}

func (c *ApiController) authorizeThread(threadID, scope string) (*object.Thread, string, string, bool) {
	org, user, ok := c.requireThreadCaller()
	if !ok {
		return nil, "", "", false
	}
	thr, err := object.DefaultThreadStore().GetThread(threadID)
	if err != nil {
		c.threadStoreErr(err)
		return nil, "", "", false
	}
	if !object.Can(thr, org, user, scope) {
		c.ResponseErrorWithStatus(http.StatusForbidden, "forbidden: caller lacks "+scope+" grant on thread "+threadID)
		return nil, "", "", false
	}
	return thr, org, user, true
}

// CreateThread creates a new durable 10M+ thread DAG owned by the caller's organization.
func (c *ApiController) CreateThread() {
	org, user, ok := c.requireThreadCaller()
	if !ok {
		return
	}
	var req struct {
		Title    string            `json:"title"`
		Metadata map[string]string `json:"metadata"`
	}
	if body := c.Body(); len(bytesTrimSpace(body)) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			c.ResponseErrorWithStatus(http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
	}
	thr, err := object.DefaultThreadStore().CreateThread(org, user, req.Title, req.Metadata)
	if err != nil {
		c.threadStoreErr(err)
		return
	}
	c.writeJSON(http.StatusOK, thr)
}

// GetThread returns thread metadata, named branches, token totals, active leases, and capability grants.
func (c *ApiController) GetThread() {
	id := strings.TrimSpace(c.Param("id"))
	thr, _, _, ok := c.authorizeThread(id, object.ThreadScopeRead)
	if !ok {
		return
	}
	c.writeJSON(http.StatusOK, thr)
}

// AppendThread appends an immutable content-addressed chunk to a thread branch with optional optimistic lease_epoch fencing.
func (c *ApiController) AppendThread() {
	id := strings.TrimSpace(c.Param("id"))
	_, org, user, ok := c.authorizeThread(id, object.ThreadScopeAppend)
	if !ok {
		return
	}
	var req struct {
		Branch     string                  `json:"branch"`
		LeaseEpoch *int64                  `json:"lease_epoch"`
		Role       string                  `json:"role"`
		Kind       string                  `json:"kind"`
		Content    string                  `json:"content"`
		ToolCalls  []object.ThreadToolCall `json:"tool_calls"`
		ToolCallID string                  `json:"tool_call_id"`
		Pinned     bool                    `json:"pinned"`
		Tokens     int                     `json:"tokens"`
		Actor      string                  `json:"actor"`
		Model      string                  `json:"model"`
		ResponseID string                  `json:"response_id"`
		Metadata   map[string]string       `json:"metadata"`
	}
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		c.ResponseErrorWithStatus(http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.Role == "" {
		req.Role = "user"
	}
	actor := strings.TrimSpace(req.Actor)
	if actor == "" {
		actor = user
	}
	branch := strings.TrimSpace(req.Branch)
	if branch == "" {
		branch = object.DefaultBranchName
	}
	chunk, epoch, err := object.DefaultThreadStore().AppendChunk(id, branch, req.LeaseEpoch, object.Chunk{
		Role:       req.Role,
		Kind:       req.Kind,
		Content:    req.Content,
		ToolCalls:  req.ToolCalls,
		ToolCallID: req.ToolCallID,
		Pinned:     req.Pinned,
		Tokens:     req.Tokens,
		Actor:      actor,
		Org:        org,
		Model:      req.Model,
		ResponseID: req.ResponseID,
		Metadata:   req.Metadata,
	})
	if err != nil {
		if errors.Is(err, object.ErrLeaseConflict) {
			c.writeJSON(http.StatusConflict, map[string]any{
				"error":         err.Error(),
				"current_epoch": epoch,
			})
			return
		}
		c.threadStoreErr(err)
		return
	}
	c.writeJSON(http.StatusOK, map[string]any{
		"thread_id":   id,
		"branch":      branch,
		"lease_epoch": epoch,
		"chunk":       chunk,
	})
}

// ForkThread creates an O(1) branch fork from any chunk or response ID on an existing branch.
func (c *ApiController) ForkThread() {
	id := strings.TrimSpace(c.Param("id"))
	_, _, _, ok := c.authorizeThread(id, object.ThreadScopeFork)
	if !ok {
		return
	}
	var req struct {
		SourceBranch string `json:"source_branch"`
		FromChunkID  string `json:"from_chunk_id"`
		Branch       string `json:"branch"`
		NewBranch    string `json:"new_branch"`
		Name         string `json:"name"`
	}
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		c.ResponseErrorWithStatus(http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	newBranch := strings.TrimSpace(req.Branch)
	if newBranch == "" {
		newBranch = strings.TrimSpace(req.NewBranch)
	}
	if newBranch == "" {
		newBranch = strings.TrimSpace(req.Name)
	}
	br, err := object.DefaultThreadStore().Fork(id, req.SourceBranch, req.FromChunkID, newBranch)
	if err != nil {
		c.threadStoreErr(err)
		return
	}
	c.writeJSON(http.StatusOK, map[string]any{
		"thread_id": id,
		"branch":    br,
	})
}

// MergeThread merges divergent chunks or a summary from a source branch into a target branch.
func (c *ApiController) MergeThread() {
	id := strings.TrimSpace(c.Param("id"))
	_, org, user, ok := c.authorizeThread(id, object.ThreadScopeMerge)
	if !ok {
		return
	}
	var req struct {
		SourceBranch string `json:"source_branch"`
		TargetBranch string `json:"target_branch"`
		Summary      string `json:"summary"`
		LeaseEpoch   *int64 `json:"lease_epoch"`
	}
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		c.ResponseErrorWithStatus(http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	target := strings.TrimSpace(req.TargetBranch)
	if target == "" {
		target = object.DefaultBranchName
	}
	chunk, err := object.DefaultThreadStore().Merge(id, req.SourceBranch, target, req.Summary, user, org, req.LeaseEpoch)
	if err != nil {
		c.threadStoreErr(err)
		return
	}
	c.writeJSON(http.StatusOK, map[string]any{
		"thread_id":     id,
		"source_branch": req.SourceBranch,
		"target_branch": target,
		"chunk":         chunk,
	})
}

// HandoffThread atomically transfers a branch lease to another agent, machine, or organization while incrementing its fencing epoch.
func (c *ApiController) HandoffThread() {
	id := strings.TrimSpace(c.Param("id"))
	_, _, _, ok := c.authorizeThread(id, object.ThreadScopeHandoff)
	if !ok {
		return
	}
	var req struct {
		Branch       string `json:"branch"`
		FromEpoch    int64  `json:"from_epoch"`
		ToActor      string `json:"to_actor"`
		ToOrg        string `json:"to_org"`
		ToMachine    string `json:"to_machine"`
		WorkspaceRef string `json:"workspace_ref"`
		GitRef       string `json:"git_ref"`
		ModelHint    string `json:"model_hint"`
		Note         string `json:"note"`
		TTLSeconds   int    `json:"ttl_seconds"`
	}
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		c.ResponseErrorWithStatus(http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	branch := strings.TrimSpace(req.Branch)
	if branch == "" {
		branch = object.DefaultBranchName
	}
	var ttl time.Duration
	if req.TTLSeconds > 0 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
	}
	lease, chunk, err := object.DefaultThreadStore().Handoff(
		id, branch, req.FromEpoch,
		req.ToActor, req.ToOrg, req.ToMachine,
		req.WorkspaceRef, req.GitRef, req.ModelHint, req.Note, ttl,
	)
	if err != nil {
		c.threadStoreErr(err)
		return
	}
	c.writeJSON(http.StatusOK, map[string]any{
		"thread_id": id,
		"branch":    branch,
		"lease":     lease,
		"chunk":     chunk,
	})
}

// AddThreadGrant grants scoped cross-account access ("org:<org>" or "user:<org>/<user>") to a thread; restricted to the thread's owner organization.
func (c *ApiController) AddThreadGrant() {
	id := strings.TrimSpace(c.Param("id"))
	org, user, ok := c.requireThreadCaller()
	if !ok {
		return
	}
	thr, err := object.DefaultThreadStore().GetThread(id)
	if err != nil {
		c.threadStoreErr(err)
		return
	}
	if !strings.EqualFold(thr.Org, org) {
		c.ResponseErrorWithStatus(http.StatusForbidden, "forbidden: only the thread owner organization may manage grants")
		return
	}
	var req struct {
		Principal  string   `json:"principal"`
		Scopes     []string `json:"scopes"`
		ExpiresAt  string   `json:"expires_at"`
		TTLSeconds int      `json:"ttl_seconds"`
	}
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		c.ResponseErrorWithStatus(http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	var exp time.Time
	if strings.TrimSpace(req.ExpiresAt) != "" {
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(req.ExpiresAt))
		if err != nil {
			c.ResponseErrorWithStatus(http.StatusBadRequest, "invalid expires_at (expected RFC3339): "+err.Error())
			return
		}
		exp = parsed
	} else if req.TTLSeconds > 0 {
		exp = time.Now().UTC().Add(time.Duration(req.TTLSeconds) * time.Second)
	}
	grantedBy := org
	if user != "" {
		grantedBy = org + "/" + user
	}
	g, err := object.DefaultThreadStore().AddGrant(id, object.Grant{
		Principal: req.Principal,
		Scopes:    req.Scopes,
		GrantedBy: grantedBy,
		ExpiresAt: exp,
	})
	if err != nil {
		c.threadStoreErr(err)
		return
	}
	c.writeJSON(http.StatusOK, map[string]any{
		"thread_id": id,
		"grant":     g,
	})
}

// DeleteThreadGrant revokes a cross-account capability grant from a thread; restricted to the thread's owner organization.
func (c *ApiController) DeleteThreadGrant() {
	id := strings.TrimSpace(c.Param("id"))
	org, _, ok := c.requireThreadCaller()
	if !ok {
		return
	}
	thr, err := object.DefaultThreadStore().GetThread(id)
	if err != nil {
		c.threadStoreErr(err)
		return
	}
	if !strings.EqualFold(thr.Org, org) {
		c.ResponseErrorWithStatus(http.StatusForbidden, "forbidden: only the thread owner organization may revoke grants")
		return
	}
	principal := strings.TrimSpace(c.Param("principal"))
	if unescaped, err := url.PathUnescape(principal); err == nil {
		principal = unescaped
	}
	if principal == "" {
		principal = strings.TrimSpace(c.Query("principal"))
	}
	if err := object.DefaultThreadStore().RevokeGrant(id, principal); err != nil {
		c.threadStoreErr(err)
		return
	}
	c.writeJSON(http.StatusOK, map[string]any{
		"thread_id": id,
		"principal": principal,
		"revoked":   true,
	})
}

// GetThreadChunk retrieves a lossless byte-range slice of any cold or hot chunk in a thread.
func (c *ApiController) GetThreadChunk() {
	id := strings.TrimSpace(c.Param("id"))
	if _, _, _, ok := c.authorizeThread(id, object.ThreadScopeRead); !ok {
		return
	}
	chunkID := strings.TrimSpace(c.Param("chunkId"))
	byteOffset, _ := strconv.Atoi(strings.TrimSpace(c.Query("byte_offset")))
	byteLimit, _ := strconv.Atoi(strings.TrimSpace(c.Query("byte_limit")))
	slice, err := object.DefaultThreadStore().GetChunk(id, chunkID, byteOffset, byteLimit)
	if err != nil {
		c.threadStoreErr(err)
		return
	}
	c.writeJSON(http.StatusOK, slice)
}

// SearchThreadChunks performs BM25 search across a thread branch's full 10M+ chunk history for lossless recall.
func (c *ApiController) SearchThreadChunks() {
	id := strings.TrimSpace(c.Param("id"))
	if _, _, _, ok := c.authorizeThread(id, object.ThreadScopeRead); !ok {
		return
	}
	var req struct {
		Branch string `json:"branch"`
		Query  string `json:"query"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		c.ResponseErrorWithStatus(http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	branch := strings.TrimSpace(req.Branch)
	if branch == "" {
		branch = object.DefaultBranchName
	}
	results, err := object.DefaultThreadStore().SearchChunks(id, branch, req.Query, req.Limit)
	if err != nil {
		c.threadStoreErr(err)
		return
	}
	c.writeJSON(http.StatusOK, map[string]any{
		"thread_id": id,
		"branch":    branch,
		"query":     req.Query,
		"results":   results,
	})
}

// ── Resumable Generation Streams (GET /v1/responses/:id/stream?after_seq=N) ──

const (
	maxFramesPerResponseStream = 8192
	maxTrackedResponseStreams  = 1024
	responseStreamRetentionTTL = 30 * time.Minute
)

// ResponseStreamFrame is one monotonically sequenced SSE frame emitted by a
// Responses generation turn.
type ResponseStreamFrame struct {
	Seq   int64  `json:"seq"`
	Event string `json:"event"`
	Data  []byte `json:"data"`
}

// ResponseStream buffers emitted SSE frames for an in-flight or recently
// completed response so disconnected clients or handoff targets can resume from
// any monotonic sequence number without losing tokens.
type ResponseStream struct {
	ResponseID string
	Org        string
	User       string
	ThreadID   string

	mu          sync.Mutex
	frames      []ResponseStreamFrame
	nextSeq     int64
	done        bool
	createdAt   time.Time
	completedAt time.Time
	notify      chan struct{}
}

func newResponseStream(responseID, org, user, threadID string) *ResponseStream {
	return &ResponseStream{
		ResponseID: responseID,
		Org:        org,
		User:       user,
		ThreadID:   threadID,
		createdAt:  time.Now().UTC(),
		notify:     make(chan struct{}),
	}
}

// Append records an emitted SSE event with a strictly monotonic sequence number
// and wakes any live tail subscribers.
func (s *ResponseStream) Append(event string, data []byte) int64 {
	s.mu.Lock()
	s.nextSeq++
	seq := s.nextSeq
	cp := make([]byte, len(data))
	copy(cp, data)
	if len(s.frames) >= maxFramesPerResponseStream {
		s.frames = s.frames[1:]
	}
	s.frames = append(s.frames, ResponseStreamFrame{
		Seq:   seq,
		Event: event,
		Data:  cp,
	})
	ch := s.notify
	s.notify = make(chan struct{})
	s.mu.Unlock()
	close(ch)
	return seq
}

// Complete marks the stream finished and wakes all live tail subscribers so
// they emit the terminal [DONE] marker and close cleanly.
func (s *ResponseStream) Complete() {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return
	}
	s.done = true
	s.completedAt = time.Now().UTC()
	ch := s.notify
	s.notify = make(chan struct{})
	s.mu.Unlock()
	close(ch)
}

// snapshotAfter returns all buffered frames with Seq > afterSeq, whether the
// stream has completed, and the notification channel for subsequent updates.
func (s *ResponseStream) snapshotAfter(afterSeq int64) (frames []ResponseStreamFrame, done bool, waitCh <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.frames {
		if f.Seq > afterSeq {
			frames = append(frames, f)
		}
	}
	return frames, s.done, s.notify
}

// ResponseStreamHub is the bounded in-process registry of resumable response streams.
type ResponseStreamHub struct {
	mu      sync.RWMutex
	streams map[string]*ResponseStream
	order   []string
}

// NewResponseStreamHub creates an empty resumable stream hub.
func NewResponseStreamHub() *ResponseStreamHub {
	return &ResponseStreamHub{
		streams: make(map[string]*ResponseStream),
	}
}

// Register creates and registers a new resumable stream keyed by responseID.
func (h *ResponseStreamHub) Register(responseID, org, user, threadID string) *ResponseStream {
	h.mu.Lock()
	defer h.mu.Unlock()
	if existing, ok := h.streams[responseID]; ok {
		return existing
	}
	h.evictLocked(time.Now().UTC())
	rs := newResponseStream(responseID, org, user, threadID)
	h.streams[responseID] = rs
	h.order = append(h.order, responseID)
	return rs
}

// Get looks up a resumable stream by responseID.
func (h *ResponseStreamHub) Get(responseID string) (*ResponseStream, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	rs, ok := h.streams[strings.TrimSpace(responseID)]
	return rs, ok
}

func (h *ResponseStreamHub) evictLocked(now time.Time) {
	if len(h.order) == 0 {
		return
	}
	kept := h.order[:0]
	for _, id := range h.order {
		rs, ok := h.streams[id]
		if !ok {
			continue
		}
		rs.mu.Lock()
		expired := rs.done && !rs.completedAt.IsZero() && now.Sub(rs.completedAt) > responseStreamRetentionTTL
		rs.mu.Unlock()
		if expired {
			delete(h.streams, id)
			continue
		}
		kept = append(kept, id)
	}
	h.order = kept
	for len(h.order) >= maxTrackedResponseStreams {
		oldest := h.order[0]
		h.order = h.order[1:]
		delete(h.streams, oldest)
	}
}

var (
	defaultStreamHubMu sync.RWMutex
	defaultStreamHub   = NewResponseStreamHub()
)

// DefaultResponseStreamHub returns the process-wide resumable stream hub.
func DefaultResponseStreamHub() *ResponseStreamHub {
	defaultStreamHubMu.RLock()
	h := defaultStreamHub
	defaultStreamHubMu.RUnlock()
	return h
}

// SetDefaultResponseStreamHub replaces the process-wide resumable stream hub (for tests).
func SetDefaultResponseStreamHub(h *ResponseStreamHub) {
	defaultStreamHubMu.Lock()
	if h == nil {
		h = NewResponseStreamHub()
	}
	defaultStreamHub = h
	defaultStreamHubMu.Unlock()
}

// StreamResponse implements GET /v1/responses/:id/stream?after_seq=N, replaying
// buffered SSE frames after after_seq and tailing live frames until [DONE].
func (c *ApiController) StreamResponse() {
	org, user, ok := c.requireThreadCaller()
	if !ok {
		return
	}
	responseID := strings.TrimSpace(c.Param("id"))
	rs, exists := DefaultResponseStreamHub().Get(responseID)
	if !exists {
		c.ResponseErrorWithStatus(http.StatusNotFound, "response stream not found: "+responseID)
		return
	}

	// Enforce ownership or explicit thread read grant.
	authorized := rs.Org == "" || strings.EqualFold(rs.Org, org)
	if !authorized && rs.ThreadID != "" {
		if thr, err := object.DefaultThreadStore().GetThread(rs.ThreadID); err == nil {
			authorized = object.Can(thr, org, user, object.ThreadScopeRead)
		}
	}
	if !authorized {
		c.ResponseErrorWithStatus(http.StatusForbidden, "forbidden: caller cannot access stream "+responseID)
		return
	}

	var afterSeq int64
	if raw := strings.TrimSpace(c.Query("after_seq")); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v < 0 {
			c.ResponseErrorWithStatus(http.StatusBadRequest, "invalid after_seq parameter")
			return
		}
		afterSeq = v
	} else if lastID := strings.TrimSpace(c.Header("Last-Event-ID")); lastID != "" {
		if v, err := strconv.ParseInt(lastID, 10, 64); err == nil && v >= 0 {
			afterSeq = v
		}
	}

	ctx := c.Context()
	c.SetHeader("Content-Type", "text/event-stream")
	c.SetHeader("Cache-Control", "no-cache")
	c.SetHeader("Connection", "keep-alive")
	c.SetHeader("X-Hanzo-Response-Id", responseID)

	_ = c.Ctx.SendStreamWriter(func(bw *bufio.Writer) {
		cursor := afterSeq
		for {
			frames, done, waitCh := rs.snapshotAfter(cursor)
			for _, f := range frames {
				if _, err := fmt.Fprintf(bw, "id: %d\nevent: %s\ndata: %s\n\n", f.Seq, f.Event, f.Data); err != nil {
					return
				}
				cursor = f.Seq
			}
			if len(frames) > 0 {
				if err := bw.Flush(); err != nil {
					return
				}
			}
			if done {
				_, _ = bw.WriteString("data: [DONE]\n\n")
				_ = bw.Flush()
				return
			}
			select {
			case <-waitCh:
			case <-ctx.Done():
				return
			}
		}
	})
}

func bytesTrimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}
