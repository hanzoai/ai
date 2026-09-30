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

// strict.go — a request whose caller can prove it reached the vendor unchanged.
//
// A comparison between two models is only worth what the requests behind it were: if
// the gateway fell back to another vendor, answered from a cache, translated the
// dialect, dropped a parameter or rewrote a message, the answer being compared is not
// the answer to the question that was asked. X-Hanzo-Strict: 1 asks for the request to
// be served exactly as sent, or refused.
//
// A strict request is served only when every one of these holds, and refused with 409
// naming the first that does not:
//
//	route_auto     the model is a concrete SKU, not auto-routing's choice
//	translation    the route's first row takes the caller's dialect as written (the relay);
//	               a family pipe, an Anthropic row and /v1/responses all rewrite it
//	ceiling_unset  the caller named the completion ceiling; the relay writes one otherwise
//	param_dropped  every field the caller sent reaches the vendor
//	rewritten      every field reaches it with the value the caller wrote
//	added          no field reaches it that the caller did not write
//
// and it is sent to the route's own first row only: no fallback, no cooled reordering.
// A vendor that refuses it is the answer.
//
// Served, it carries X-Hanzo-Request-Sha256 and X-Hanzo-Upstream-Sha256: SHA-256 over
// the RFC 8785 form of the body the caller sent and of the body the vendor was sent,
// each without the two fields this gateway writes on every request — the model (each
// vendor's own id for the SKU) and stream_options.include_usage (a stream is billed
// from the usage it asks for). They are equal on every strict answer, and the
// X-Hanzo-Served beside them is the SKU asked for.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-json-experiment/json/jsontext"
)

const (
	strictHeader    = "X-Hanzo-Strict"
	violationHeader = "X-Hanzo-Strict-Violation"
	requestSha      = "X-Hanzo-Request-Sha256"
	upstreamSha     = "X-Hanzo-Upstream-Sha256"
)

// strictAsked reports a request that asked to be served unchanged or not at all.
func (c *ApiController) strictAsked() bool {
	return c.Header(strictHeader) == "1"
}

// refuseStrict answers a strict request that cannot be served as sent: 409, the
// invariant it broke in a header a program reads, and why in words a person reads.
func (c *ApiController) refuseStrict(invariant, why string) {
	c.SetHeader(strictHeader, "1")
	c.SetHeader(violationHeader, invariant)
	c.ResponseFailure(&apiError{status: http.StatusConflict, code: "strict_violation",
		msg: fmt.Sprintf("strict: %s: %s", invariant, why)})
}

// digest is the SHA-256 of body's RFC 8785 form, without the model and
// stream_options.include_usage. A body with a repeated name is not canonical, and is
// refused rather than hashed as whichever copy a parser happens to keep.
func digest(body []byte) (string, error) {
	fields, err := strictFields(body)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(fields)
	if err != nil {
		return "", err
	}
	v := jsontext.Value(b)
	if err := v.Canonicalize(); err != nil {
		return "", err
	}
	sum := sha256.Sum256(v)
	return hex.EncodeToString(sum[:]), nil
}

// strictFields is body's top level, checked for repeated names, with the two fields
// the gateway writes on every request taken out.
func strictFields(body []byte) (map[string]json.RawMessage, error) {
	whole := jsontext.Value(append([]byte(nil), body...))
	if err := whole.Canonicalize(); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, err
	}
	delete(fields, "model")
	if raw, ok := fields["stream_options"]; ok {
		var opts map[string]json.RawMessage
		if err := json.Unmarshal(raw, &opts); err != nil {
			return nil, err
		}
		delete(opts, "include_usage")
		if len(opts) == 0 {
			delete(fields, "stream_options")
		} else {
			b, err := json.Marshal(opts)
			if err != nil {
				return nil, err
			}
			fields["stream_options"] = b
		}
	}
	return fields, nil
}

// unchanged compares what the caller sent with what the vendor would be sent and
// names the first difference as the invariant it breaks, or returns "" and both
// digests when there is none.
func unchanged(client, upstream []byte) (invariant, field, clientSha, upstreamShaSum string, err error) {
	sent, err := strictFields(client)
	if err != nil {
		return "", "", "", "", err
	}
	relayed, err := strictFields(upstream)
	if err != nil {
		return "", "", "", "", err
	}
	for k, v := range sent {
		w, ok := relayed[k]
		if !ok {
			return "param_dropped", k, "", "", nil
		}
		if !sameJSON(v, w) {
			return "rewritten", k, "", "", nil
		}
	}
	for k := range relayed {
		if _, ok := sent[k]; !ok {
			return "added", k, "", "", nil
		}
	}
	if clientSha, err = digest(client); err != nil {
		return "", "", "", "", err
	}
	if upstreamShaSum, err = digest(upstream); err != nil {
		return "", "", "", "", err
	}
	if clientSha != upstreamShaSum {
		return "rewritten", "", "", "", nil
	}
	return "", "", clientSha, upstreamShaSum, nil
}

// sameJSON compares two JSON values by their RFC 8785 form, so key order, whitespace
// and a number's spelling do not count as a change.
func sameJSON(a, b json.RawMessage) bool {
	x, y := jsontext.Value(append([]byte(nil), a...)), jsontext.Value(append([]byte(nil), b...))
	if x.Canonicalize() != nil || y.Canonicalize() != nil {
		return false
	}
	return string(x) == string(y)
}
