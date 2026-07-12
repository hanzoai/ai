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

package controllers

import (
	"fmt"
	"io"
	"strings"

	"github.com/beego/beego/logs"
	"github.com/hanzoai/ai/model"
	"github.com/hanzoai/ai/object"
)

// isRetryableError returns true if the error message indicates a transient or
// provider-side failure that warrants trying a fallback provider.
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())

	// HTTP status codes embedded in error messages from upstream providers
	retryableSubstrings := []string{
		// Auth / access on THIS provider — cascade to the next provider's key.
		"401", "unauthorized",
		"402", "payment required",
		// 403 / agreement-gated: a model the provider won't serve this account
		// (DO GenAI returns 403 "this model is not available for your account" for
		// upstream-agreement models). Cascading is what makes the do-ai -> anthropic
		// opus fallback actually fire instead of hard-failing on the DO 403.
		"403", "forbidden", "not available for your account",
		"429", "rate limit", "too many requests",
		"500", "internal server error",
		"502", "bad gateway",
		"503", "service unavailable",
		"504", "gateway timeout",
		"timeout", "deadline exceeded",
		"connection refused", "connection reset",
		"eof", // unexpected connection close
		// A disabled (admin-toggled-off) or unconfigured primary provider is,
		// from the caller's perspective, a service-unavailable condition that a
		// fallback provider should transparently handle. callProvider surfaces it
		// as "... is unavailable ..." (see below) so the failover loop advances to
		// the next fallback instead of hard-failing on a disabled primary.
		"unavailable",
		// Credit / quota exhaustion: a provider whose FREE CREDIT or paid quota is
		// spent must cascade to the next provider in the fallback chain, so
		// credit-first routing degrades to paid / paid-only instead of a hard fail
		// (OpenAI "insufficient_quota", generic "quota"/"credit"/"billing limit").
		// Deliberately NOT bare "exceeded" — that also matches request-size errors
		// ("maximum context length ... exceeded") which must NOT cascade.
		"quota", "insufficient", "credit", "billing",
	}
	for _, sub := range retryableSubstrings {
		if strings.Contains(msg, sub) {
			return true
		}
	}
	return false
}

// failoverQueryText tries the primary provider, then each fallback in order.
// It returns the first successful result. If all providers fail, it returns
// the last error. The providerName output indicates which provider succeeded.
//
// The writer is only usable for one attempt (streaming writes are one-shot),
// so failover is only attempted when the primary fails before writing any
// response data. For non-streaming, the writer buffers internally and a fresh
// writer is created per attempt by the caller. For streaming, failover is
// only possible if no bytes have been flushed to the client yet.
func failoverQueryText(
	route *modelRoute,
	question string,
	writer io.Writer,
	history []*model.RawMessage,
	knowledge []*model.RawMessage,
	lang string,
	writerHasData func() bool,
) (*model.ModelResult, string, error) {
	// Try primary provider
	result, err := callProvider(route.providerName, route.upstreamModel, question, writer, history, knowledge, lang)
	if err == nil {
		return result, route.providerName, nil
	}

	// If the writer already sent data to the client (streaming), we cannot
	// retry — the response is partially committed.
	if writerHasData != nil && writerHasData() {
		logs.Warn("failover: primary provider %s failed after partial write, cannot retry: %v",
			route.providerName, err)
		return nil, route.providerName, err
	}

	// Check if the error is retryable
	if !isRetryableError(err) {
		logs.Warn("failover: primary provider %s failed with non-retryable error: %v",
			route.providerName, err)
		return nil, route.providerName, err
	}

	if len(route.fallbacks) == 0 {
		return nil, route.providerName, err
	}

	logs.Warn("failover: primary provider %s failed (%v), trying %d fallback(s)",
		route.providerName, err, len(route.fallbacks))

	var lastErr error = err
	for i, fb := range route.fallbacks {
		logs.Info("failover: attempting fallback[%d] provider=%s upstream=%s",
			i, fb.providerName, fb.upstreamModel)

		result, fbErr := callProvider(fb.providerName, fb.upstreamModel, question, writer, history, knowledge, lang)
		if fbErr == nil {
			logs.Info("failover: fallback[%d] provider=%s succeeded", i, fb.providerName)
			return result, fb.providerName, nil
		}

		logs.Warn("failover: fallback[%d] provider=%s failed: %v", i, fb.providerName, fbErr)
		lastErr = fbErr

		// If this fallback also wrote partial data, stop trying
		if writerHasData != nil && writerHasData() {
			break
		}

		// Only retry on retryable errors
		if !isRetryableError(fbErr) {
			break
		}
	}

	return nil, route.providerName, lastErr
}

// callProvider creates a model provider from the DB-stored provider entry and
// calls QueryText. This is the same flow as the existing code in the OpenAI
// and Anthropic handlers, extracted for reuse by the failover loop.
func callProvider(
	providerName string,
	upstreamModel string,
	question string,
	writer io.Writer,
	history []*model.RawMessage,
	knowledge []*model.RawMessage,
	lang string,
) (*model.ModelResult, error) {
	provider, err := object.GetModelProviderByName(providerName)
	if err != nil {
		return nil, err
	}
	if provider == nil {
		// GetModelProviderByName returns nil for BOTH a missing provider and a
		// disabled (State != "Active") Model provider. Word this as "unavailable"
		// so isRetryableError classifies it retryable and the failover loop tries
		// the next fallback — this is how route.fallbacks stays honored when the
		// primary provider is toggled off via /v1/admin/providers.
		return nil, fmt.Errorf("provider %q is unavailable (disabled or not configured)", providerName)
	}

	provider.SubType = upstreamModel

	modelProvider, err := provider.GetModelProvider(lang)
	if err != nil {
		return nil, err
	}

	return modelProvider.QueryText(question, writer, history, "", knowledge, nil, lang)
}
