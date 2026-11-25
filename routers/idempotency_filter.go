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

package routers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/hanzoai/ai/controllers"
	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
	"github.com/zap-proto/zip"
)

const (
	headerIdempotencyKey               = "Idempotency-Key"
	headerExecutionID                  = "X-Execution-Id"
	headerIdempotencyReplayed          = "X-Idempotency-Replayed"
	headerIdempotencyReplayUnavailable = "X-Idempotency-Replay-Unavailable"
	headerIdempotencySideEffects       = "X-Idempotency-Side-Effects-Committed"
)

// allowlistedReplayHeaders bounds which response headers may be persisted and
// replayed, preventing sensitive headers (Set-Cookie, Authorization) or
// unbounded header sets from being stored or leaked.
var allowlistedReplayHeaders = []string{
	"Content-Type",
	"X-Routed-Model",
	"X-Request-Id",
	"Location",
	"ETag",
	"Cache-Control",
}

// IdempotencyMiddleware enforces durable, IAM-scoped idempotency with leases,
// monotonic fencing tokens, bounded response replay, and fail-closed store
// semantics for mutating HTTP requests carrying an Idempotency-Key header.
type IdempotencyMiddleware struct {
	store   *object.IdempotencyStore
	ownerID string
	// beforeRunning is an optional test hook invoked after ClaimOwned (CLAIMED)
	// and before MarkRunning (RUNNING). Nil in production.
	beforeRunning func(ctx context.Context, rec *object.IdempotencyRecord) error
}

// NewIdempotencyMiddleware builds a middleware instance backed by store with a
// distinct replica/worker ownerID.
func NewIdempotencyMiddleware(store *object.IdempotencyStore, ownerID string) *IdempotencyMiddleware {
	if store == nil {
		store = object.DefaultIdempotencyStore()
	}
	if ownerID == "" {
		host, _ := os.Hostname()
		ownerID = host + ":" + uuid.NewString()
	}
	return &IdempotencyMiddleware{
		store:   store,
		ownerID: ownerID,
	}
}

var (
	defaultIdemMWOnce sync.Once
	defaultIdemMW     *IdempotencyMiddleware
)

// DefaultIdempotencyMiddleware returns the process-wide middleware wired into
// InstallFilters, backed by object.DefaultIdempotencyStore().
func DefaultIdempotencyMiddleware() *IdempotencyMiddleware {
	defaultIdemMWOnce.Do(func() {
		defaultIdemMW = NewIdempotencyMiddleware(object.DefaultIdempotencyStore(), "")
	})
	return defaultIdemMW
}

// IdempotencyFilter is the canonical filter registered in InstallFilters.
func IdempotencyFilter(c *zip.Ctx) error {
	return DefaultIdempotencyMiddleware().Handle(c)
}

// Handle enforces durable idempotency for POST/PUT/PATCH/DELETE requests that
// present an Idempotency-Key header.
func (m *IdempotencyMiddleware) Handle(c *zip.Ctx) error {
	if !isMutatingMethod(c.Method()) {
		return c.Continue()
	}

	hdrVals := c.Fiber().Request().Header.PeekAll(headerIdempotencyKey)
	if len(hdrVals) == 0 {
		return c.Continue()
	}
	if len(hdrVals) > 1 {
		return writeIdemError(c, http.StatusBadRequest, "invalid_idempotency_key", "multiple Idempotency-Key headers are not allowed", nil)
	}
	rawKey := string(hdrVals[0])

	cfg := m.store.Config()
	if err := object.ValidateIdempotencyKey(rawKey, cfg.MaxKeyLength); err != nil {
		return writeIdemError(c, http.StatusBadRequest, "invalid_idempotency_key", err.Error(), nil)
	}

	// Resolve IAM-verified scope — NEVER derive org/principal from unverified headers.
	scope, ok := resolveIdempotencyScope(c)
	if !ok {
		c.SetHeader("WWW-Authenticate", "Bearer")
		return writeIdemError(c, http.StatusUnauthorized, "idempotency_auth_required", "verified authentication is required for idempotent mutations", nil)
	}

	rawBody := c.Body()
	if len(rawBody) > cfg.MaxFingerprintBodyBytes {
		return writeIdemError(c, http.StatusRequestEntityTooLarge, "idempotency_body_too_large", "request body exceeds maximum size for idempotency fingerprinting", nil)
	}

	normalizedRoute := canonicalRoutePath(c.Path())
	scope.Operation = strings.ToUpper(c.Method()) + " " + normalizedRoute

	queryParams := parseQueryParams(c)
	reqHash := object.ComputeRequestFingerprint(c.Method(), normalizedRoute, queryParams, c.Header("Content-Type"), rawBody)
	reqStreaming := isStreamingRequestPayload(rawBody)

	ctx := c.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	claimReq := object.IdempotencyClaimRequest{
		Scope:          scope,
		IdempotencyKey: rawKey,
		RequestHash:    reqHash,
		OwnerID:        m.ownerID,
		Streaming:      reqStreaming,
	}

	res, err := m.store.Claim(ctx, claimReq)
	if err != nil {
		return handleClaimError(c, err)
	}

	// If another worker currently holds an active lease, wait via synchronized
	// channel/KV notification (zero unconditional polling sleep) up to timeout.
	if res.Outcome == object.ClaimInFlight {
		waited, done, werr := m.store.WaitForCompletion(ctx, res.Record.ScopeHash, res.Record.IdempotencyKey, cfg.InFlightWaitTimeout)
		if werr != nil {
			if errors.Is(werr, object.ErrIdempotencyStoreUnavailable) {
				return writeIdemError(c, http.StatusServiceUnavailable, "idempotency_store_unavailable", "authoritative idempotency store is unavailable", nil)
			}
			return writeIdemError(c, http.StatusConflict, "idempotency_in_progress", "request with this Idempotency-Key is currently in progress", map[string]any{
				"execution_id": res.Record.ExecutionID,
				"state":        res.Record.State,
			})
		}
		if done && waited != nil {
			res = outcomeFromRecord(waited)
		} else {
			// Lease may have expired while waiting: attempt one re-claim to either
			// safely take over an expired CLAIMED lease or move expired RUNNING -> UNKNOWN.
			res2, err2 := m.store.Claim(ctx, claimReq)
			if err2 != nil {
				return handleClaimError(c, err2)
			}
			res = res2
		}
	}

	switch res.Outcome {
	case object.ClaimCompleted:
		return writeCompletedReplay(c, res.Record)

	case object.ClaimFailed:
		return writeFailedReplay(c, res.Record)

	case object.ClaimUnknown:
		return writeUnknownOutcome(c, res.Record)

	case object.ClaimInFlight:
		c.SetHeader("Retry-After", "1")
		c.SetHeader(headerExecutionID, res.Record.ExecutionID)
		return writeIdemError(c, http.StatusConflict, "idempotency_in_progress", "request with this Idempotency-Key is currently in progress", map[string]any{
			"execution_id": res.Record.ExecutionID,
			"state":        res.Record.State,
		})

	case object.ClaimOwned:
		return m.executeOwned(c, ctx, res.Record, reqStreaming)
	}

	return writeIdemError(c, http.StatusServiceUnavailable, "idempotency_store_unavailable", "unexpected idempotency claim outcome", nil)
}

func (m *IdempotencyMiddleware) executeOwned(c *zip.Ctx, ctx context.Context, rec *object.IdempotencyRecord, reqStreaming bool) error {
	cfg := m.store.Config()

	exec := object.NewIdempotencyExecution(m.store, rec)

	c.SetContext(object.WithIdempotencyExecution(ctx, exec))
	c.SetHeader(headerExecutionID, rec.ExecutionID)
	c.SetHeader(headerIdempotencyReplayed, "false")

	// Start bounded lease renewal heartbeat so long-running executions keep their
	// lease alive and stop immediately on exit with zero goroutine leaks.
	hbCtx, hbCancel := context.WithCancel(context.Background())
	hbDone := make(chan struct{})
	renewEvery := cfg.LeaseTTL / 3
	if renewEvery < 10*time.Millisecond {
		renewEvery = 10 * time.Millisecond
	}
	go func() {
		defer close(hbDone)
		ticker := time.NewTicker(renewEvery)
		defer ticker.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-ticker.C:
				if err := m.store.RenewLease(hbCtx, rec.ScopeHash, rec.IdempotencyKey, rec.FenceToken); err != nil {
					return
				}
			}
		}
	}()
	stopHeartbeat := func() {
		hbCancel()
		<-hbDone
	}
	defer stopHeartbeat()

	// Optional test hook between CLAIMED and RUNNING (allows deterministic crash /
	// preemption simulation before side effects begin).
	if m.beforeRunning != nil {
		if err := m.beforeRunning(ctx, rec); err != nil {
			return err
		}
	}

	// Transition CLAIMED -> RUNNING before invoking the handler. If fenced out
	// (lease expired and another worker took over), abort BEFORE executing!
	if err := m.store.MarkRunning(ctx, rec.ScopeHash, rec.IdempotencyKey, rec.FenceToken); err != nil {
		if errors.Is(err, object.ErrIdempotencyFenced) {
			return writeIdemError(c, http.StatusConflict, "idempotency_fenced", "execution lease was superseded before handler ran", map[string]any{
				"execution_id": rec.ExecutionID,
			})
		}
		return writeIdemError(c, http.StatusServiceUnavailable, "idempotency_store_unavailable", "failed to mark idempotency execution running", nil)
	}

	completed := false
	defer func() {
		if r := recover(); r != nil {
			if !completed {
				_ = m.store.MarkUnknown(context.Background(), rec.ScopeHash, rec.IdempotencyKey, rec.FenceToken,
					"handler_panic", "handler panicked while execution was running; reconciliation required", exec.IsSideEffectsCommitted())
			}
			panic(r)
		}
	}()

	handlerErr := c.Continue()
	completed = true

	resp := c.Fiber().Response()
	status := resp.StatusCode()
	if status == 0 {
		if handlerErr != nil {
			status = http.StatusInternalServerError
		} else {
			status = http.StatusOK
		}
	}

	ct := string(resp.Header.ContentType())
	isStream := reqStreaming || resp.IsBodyStream() || strings.HasPrefix(strings.ToLower(strings.TrimSpace(ct)), "text/event-stream")
	streamDisc := exec.IsStreamDisconnected() || errors.Is(ctx.Err(), context.Canceled)

	var respBody []byte
	if !isStream {
		respBody = append([]byte(nil), resp.Body()...)
	}

	headers := captureAllowlistedHeaders(c)
	headers[headerExecutionID] = rec.ExecutionID

	sideEffectsCommitted := exec.IsSideEffectsCommitted()
	outcome := object.IdempotencyOutcome{
		Status:               status,
		Headers:              headers,
		Body:                 respBody,
		Streaming:            isStream,
		StreamDisconnected:   isStream && streamDisc,
		SideEffectsCommitted: sideEffectsCommitted,
	}

	// Distinguish ambiguous context timeouts/cancellations where no response was
	// committed vs explicit failures vs completed responses.
	var commitErr error
	switch {
	case handlerErr != nil && errors.Is(handlerErr, context.DeadlineExceeded) && !isStream:
		commitErr = m.store.MarkUnknown(context.Background(), rec.ScopeHash, rec.IdempotencyKey, rec.FenceToken,
			"execution_timeout", "execution timed out with unknown outcome; reconciliation required", sideEffectsCommitted)
	case handlerErr != nil || status >= http.StatusInternalServerError:
		outcome.ErrorCode = "execution_failed"
		if handlerErr != nil {
			outcome.ErrorMessage = handlerErr.Error()
		} else {
			outcome.ErrorMessage = http.StatusText(status)
		}
		commitErr = m.store.Fail(context.Background(), rec.ScopeHash, rec.IdempotencyKey, rec.FenceToken, outcome)
	default:
		if isStream {
			outcome.SideEffectsCommitted = true
		}
		commitErr = m.store.Complete(context.Background(), rec.ScopeHash, rec.IdempotencyKey, rec.FenceToken, outcome)
	}

	if commitErr != nil {
		if errors.Is(commitErr, object.ErrIdempotencyFenced) {
			log.Warn("idempotency: stale worker %s fenced on commit key=%s exec=%s fence=%d",
				m.ownerID, rec.IdempotencyKey, rec.ExecutionID, rec.FenceToken)
		} else {
			log.Error("idempotency: failed to commit outcome key=%s exec=%s: %v",
				rec.IdempotencyKey, rec.ExecutionID, commitErr)
		}
	}

	return handlerErr
}

// ── Scope resolution (IAM-verified ONLY) ─────────────────────────────────────

func resolveIdempotencyScope(c *zip.Ctx) (object.IdempotencyScope, bool) {
	// 1. In-process caller stamped by host binary (ai.For) — cannot be spoofed over HTTP.
	if who, ok := object.CallerOf(c.Context()); ok {
		org := strings.TrimSpace(who.Org)
		if org == "" {
			return object.IdempotencyScope{}, false
		}
		principal := strings.TrimSpace(who.Person)
		if principal == "" {
			principal = "caller:" + org
		}
		return object.IdempotencyScope{
			Org:       org,
			Principal: principal,
			Project:   strings.TrimSpace(who.Project),
			AuthScope: "inproc",
		}, true
	}

	// 2. IAM-verified session or Bearer (signed JWT or IAM-verified sk- API key).
	if user := sessionOrBearerUser(c); user != nil && user.Owner != "" && user.Name != "" {
		org := GetOrg(c)
		if org == "" {
			org = user.Owner
		}
		principal := util.GetIdFromOwnerAndName(user.Owner, user.Name)
		project := strings.TrimSpace(c.Header("X-Project-Id"))
		return object.IdempotencyScope{
			Org:       org,
			Principal: principal,
			Project:   project,
			AuthScope: deriveVerifiedAuthScope(c, user),
		}, true
	}

	// 3. Verified publishable key (pk-*) resolved via IAM to its owning org.
	if tok := credential(c); strings.HasPrefix(tok, "pk-") {
		org, err := controllers.PublishableOrg(tok)
		if err == nil && org != "" {
			short := shortSHA256(tok)
			return object.IdempotencyScope{
				Org:       org,
				Principal: "pk:" + short,
				Project:   strings.TrimSpace(c.Header("X-Project-Id")),
				AuthScope: "pk:" + short,
			}, true
		}
	}

	// No verified principal — NEVER fall back to X-Org-Id or X-User-Id headers.
	return object.IdempotencyScope{}, false
}

func deriveVerifiedAuthScope(c *zip.Ctx, user *iam.User) string {
	var parts []string
	if util.IsSuperAdmin(user) {
		parts = append(parts, "superadmin")
	}
	if keyID := strings.TrimSpace(c.Header("X-Key-Id")); keyID != "" {
		parts = append(parts, "key:"+keyID)
	} else if tok := credential(c); tok != "" {
		if isJwtLike(tok) {
			if user.Type != "" {
				parts = append(parts, "jwt:"+user.Type)
			} else {
				parts = append(parts, "jwt")
			}
		} else {
			parts = append(parts, "apikey:"+shortSHA256(tok))
		}
	} else {
		parts = append(parts, "session")
	}
	return strings.Join(parts, "|")
}

func shortSHA256(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

// ── Response writers & helpers ───────────────────────────────────────────────

func handleClaimError(c *zip.Ctx, err error) error {
	switch {
	case errors.Is(err, object.ErrIdempotencyConflict):
		return writeIdemError(c, http.StatusConflict, "idempotency_key_conflict", "Idempotency-Key was already used with different request parameters", nil)
	case errors.Is(err, object.ErrIdempotencyInvalidKey):
		return writeIdemError(c, http.StatusBadRequest, "invalid_idempotency_key", err.Error(), nil)
	case errors.Is(err, object.ErrIdempotencyCardinalityExceeded):
		c.SetHeader("Retry-After", "60")
		return writeIdemError(c, http.StatusTooManyRequests, "idempotency_cardinality_exceeded", "active idempotency key limit exceeded for organization", nil)
	case errors.Is(err, object.ErrIdempotencyStoreUnavailable):
		return writeIdemError(c, http.StatusServiceUnavailable, "idempotency_store_unavailable", "authoritative idempotency store is unavailable", nil)
	default:
		return writeIdemError(c, http.StatusServiceUnavailable, "idempotency_store_unavailable", "idempotency check failed", nil)
	}
}

func outcomeFromRecord(rec *object.IdempotencyRecord) *object.IdempotencyClaimResult {
	switch rec.State {
	case object.IdemStateCompleted:
		return &object.IdempotencyClaimResult{Outcome: object.ClaimCompleted, Record: rec}
	case object.IdemStateFailed:
		return &object.IdempotencyClaimResult{Outcome: object.ClaimFailed, Record: rec}
	case object.IdemStateUnknown:
		return &object.IdempotencyClaimResult{Outcome: object.ClaimUnknown, Record: rec}
	default:
		return &object.IdempotencyClaimResult{Outcome: object.ClaimInFlight, Record: rec}
	}
}

func writeCompletedReplay(c *zip.Ctx, rec *object.IdempotencyRecord) error {
	c.SetHeader(headerExecutionID, rec.ExecutionID)
	if !rec.ResponseRetained {
		reason := rec.NonReplayReason
		if reason == "" {
			reason = "not_retained"
		}
		c.SetHeader(headerIdempotencyReplayed, "false")
		c.SetHeader(headerIdempotencyReplayUnavailable, reason)
		return writeIdemError(c, http.StatusConflict, "idempotency_response_not_retained",
			"original request completed but its response body was not retained for replay; do not re-execute blindly",
			map[string]any{
				"execution_id":         rec.ExecutionID,
				"billing_ref":          rec.BillingRef,
				"state":                rec.State,
				"original_status":      rec.ResponseStatus,
				"reason":               reason,
				"streaming":            rec.Streaming,
				"stream_disconnected":  rec.StreamDisconnected,
				"replayable":           false,
				"completed":            true,
				"side_effects_applied": rec.SideEffectsCommitted || rec.ResponseStatus < http.StatusInternalServerError,
			})
	}

	applyStoredHeaders(c, rec)
	c.SetHeader(headerIdempotencyReplayed, "true")
	status := rec.ResponseStatus
	if status == 0 {
		status = http.StatusOK
	}
	c.Status(status)
	c.Fiber().Response().SetBody(rec.ResponseBody)
	return nil
}

func writeFailedReplay(c *zip.Ctx, rec *object.IdempotencyRecord) error {
	c.SetHeader(headerExecutionID, rec.ExecutionID)
	if rec.SideEffectsCommitted {
		c.SetHeader(headerIdempotencySideEffects, "true")
	}
	if rec.ResponseRetained && len(rec.ResponseBody) > 0 {
		applyStoredHeaders(c, rec)
		c.SetHeader(headerIdempotencyReplayed, "true")
		status := rec.ResponseStatus
		if status == 0 {
			status = http.StatusInternalServerError
		}
		c.Status(status)
		c.Fiber().Response().SetBody(rec.ResponseBody)
		return nil
	}

	status := rec.ResponseStatus
	if status == 0 {
		status = http.StatusInternalServerError
	}
	c.SetHeader(headerIdempotencyReplayed, "true")
	return writeIdemError(c, status, firstNonEmpty(rec.ErrorCode, "execution_failed"),
		firstNonEmpty(rec.ErrorMessage, "original execution failed"),
		map[string]any{
			"execution_id":           rec.ExecutionID,
			"billing_ref":            rec.BillingRef,
			"state":                  rec.State,
			"side_effects_committed": rec.SideEffectsCommitted,
			"replayable":             false,
		})
}

func writeUnknownOutcome(c *zip.Ctx, rec *object.IdempotencyRecord) error {
	c.SetHeader(headerExecutionID, rec.ExecutionID)
	c.SetHeader(headerIdempotencyReplayed, "false")
	c.SetHeader(headerIdempotencyReplayUnavailable, "unknown_outcome")
	return writeIdemError(c, http.StatusConflict, "idempotency_execution_unknown",
		firstNonEmpty(rec.ErrorMessage, "previous execution outcome is unknown and requires reconciliation before retrying"),
		map[string]any{
			"execution_id":            rec.ExecutionID,
			"billing_ref":             rec.BillingRef,
			"state":                   rec.State,
			"reconciliation_required": true,
			"side_effects_committed":  rec.SideEffectsCommitted,
		})
}

func applyStoredHeaders(c *zip.Ctx, rec *object.IdempotencyRecord) {
	for k, v := range object.DecodeResponseHeaders(rec.ResponseHeaders) {
		if k != "" && v != "" {
			c.SetHeader(k, v)
		}
	}
	c.SetHeader(headerExecutionID, rec.ExecutionID)
}

func captureAllowlistedHeaders(c *zip.Ctx) map[string]string {
	out := make(map[string]string, len(allowlistedReplayHeaders))
	hdr := &c.Fiber().Response().Header
	for _, name := range allowlistedReplayHeaders {
		if val := strings.TrimSpace(string(hdr.Peek(name))); val != "" {
			out[name] = val
		}
	}
	return out
}

func writeIdemError(c *zip.Ctx, status int, code, message string, extra map[string]any) error {
	errObj := map[string]any{
		"code":    code,
		"message": message,
	}
	for k, v := range extra {
		errObj[k] = v
	}
	return c.JSON(status, map[string]any{"error": errObj})
}

func isMutatingMethod(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

func canonicalRoutePath(rawPath string) string {
	if rawPath == "" {
		return "/"
	}
	return strings.ToLower(path.Clean(rawPath))
}

func parseQueryParams(c *zip.Ctx) url.Values {
	raw := c.Fiber().Request().URI().QueryString()
	if len(raw) == 0 {
		return nil
	}
	vals, err := url.ParseQuery(string(raw))
	if err != nil {
		return url.Values{"_raw": []string{string(raw)}}
	}
	return vals
}

func isStreamingRequestPayload(body []byte) bool {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return false
	}
	var probe struct {
		Stream bool `json:"stream"`
	}
	if err := json.Unmarshal(trimmed, &probe); err != nil {
		return false
	}
	return probe.Stream
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
