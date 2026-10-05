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
	"context"
	"encoding/json"
)

// A decision's captured states: what an org that teaches Kai from its mistakes
// (the host's /v1/capabilities) trains on without the decision service encoding
// the request again. The host says which orgs capture and files what each
// decision captured; the service's `capture` object never reaches the caller.
// Without a host nothing is captured.

// Capture is one decision's captured states.
type Capture struct {
	// Org is the org that paid for the decision and is answered for.
	Org string
	// Decision is the decision's id, as the caller was answered it.
	Decision string
	// Request is the request id it was answered under.
	Request string
	// Model is the model the caller asked for.
	Model string
	// States is the decision service's `capture` object.
	States json.RawMessage
}

// CapturingFunc reports whether org's decisions carry their states.
type CapturingFunc func(ctx context.Context, org string) bool

// CaptureFunc files one decision's states. It is called on the request path
// after the answer is ready, so it must hand the work off and return.
type CaptureFunc func(ctx context.Context, c Capture)

var (
	capturing CapturingFunc
	captured  CaptureFunc
)

// SetCapture installs the host's capture (nil clears it).
func SetCapture(on CapturingFunc, file CaptureFunc) { capturing, captured = on, file }

// Capturing reports whether org's decisions carry their states: never without a
// host, never for no org.
func Capturing(ctx context.Context, org string) bool {
	return capturing != nil && captured != nil && org != "" && capturing(ctx, org)
}

// FileCapture files c with the host.
func FileCapture(ctx context.Context, c Capture) {
	if captured != nil {
		captured(ctx, c)
	}
}
