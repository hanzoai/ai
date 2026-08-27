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
	"errors"
	"github.com/hanzoai/ai/object"
	"net/http"

	openai "github.com/hanzoai/go-openai"
)

// Upstream failures carry an HTTP status. This is the ONE place that extracts
// it, and the ONE place (wrapUpstreamError) that types it — as the existing sum
// type apiError — so every downstream decision (retry / classify / render) folds
// over the same typed value instead of sniffing three copies of the same
// substring list. Decomplected: the status is a property of the error, not a
// guess re-derived at each call site.
//
// The go-openai SDK returns *openai.APIError with an HTTPStatusCode field for
// every provider HTTP failure (429 "Platform overloaded", 403 agreement-gated,
// 5xx, …). Without this wrapper those statuses are lost into a plain error
// string — which is how a transient upstream 429 used to become a hard,
// untyped, fatal-looking 500 at the client edge (the "it stops" failure).

// upstreamHTTPStatus returns the HTTP status carried by a provider/SDK error, or
// 0 for a transport/decode error that carries none.
//
// It reads both spellings of the same fact — the SDK's *openai.APIError and the
// *apiError wrapUpstreamError restates it as — so a status survives being
// wrapped and every reader gets the same answer whichever side of the wrapper it
// stands on.
//
// Distinct from statusOf, which answers a different question ("what should the
// CLIENT see") and therefore defaults to 401 for an unrecognised error. That
// default is correct for rendering and catastrophic for attribution: it would
// file every connection reset as an auth failure, and faultOf would refuse to
// fail over. Absence is reported as 0 here so the caller can tell "no status"
// from "some status".
func upstreamHTTPStatus(err error) int {
	var oe *openai.APIError
	if errors.As(err, &oe) {
		return oe.HTTPStatusCode
	}
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.status
	}
	return 0
}

// wrapUpstreamError types a provider/SDK error with its HTTP status as an
// apiError (the package's existing error sum type), so statusOf, isRetryableError
// and the wire error-mappers all read ONE typed value. A transport error with no
// status is returned unwrapped (statusOf then fails secure per its contract).
func wrapUpstreamError(err error) error {
	if err == nil {
		return nil
	}
	if status := upstreamHTTPStatus(err); status != 0 {
		// A vendor's rejection text is its own words, and a 401 from one names the
		// key it refused. This reaches the caller through ResponseFailure, so the
		// key is taken out before it becomes a message.
		return &apiError{status: status, msg: object.RedactKeys(err.Error())}
	}
	return err
}

// statusForModelError is the status a CLIENT should see for a model-call
// failure. Upstream HTTP failures keep their real status (a 429 stays a 429 so
// the client retries; a 403 stays a 403). A model-call error is never an auth
// failure FROM THE CLIENT'S PERSPECTIVE — its own credential was already
// validated upstream — so statusOf's auth-fail-secure default (401) is mapped to
// 500 here. This is the one place that distinction is made.
//
// 402 is the same argument about money instead of identity, and it is the more
// expensive one to get wrong. A vendor's 402 says OUR account with that vendor is
// spent; forwarded, it tells a funded customer they are out of credit and sends
// every client that acts on a 402 to fix a bill that is not theirs. 503 is the
// true statement, and the same one exhausted() makes.
func statusForModelError(err error) int {
	switch s := statusOf(err); s {
	case http.StatusUnauthorized:
		return http.StatusInternalServerError
	case http.StatusPaymentRequired:
		return http.StatusServiceUnavailable
	default:
		return s
	}
}
