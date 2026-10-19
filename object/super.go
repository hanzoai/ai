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
	"sync/atomic"

	"github.com/zap-proto/zip"

	iam "github.com/hanzoai/ai/internal/iam"
)

// super is the host's SuperAdmin verdict (SetSuper).
var super atomic.Pointer[func(*zip.Ctx) bool]

// SetSuper installs the HOST's answer to "is this request's principal the
// SuperAdmin". SuperAdmin is a person whose own org is the reserved admin org, and
// the host's identity boundary decides it once per request, from the token it
// validated (authz.Claims.Sudo), after deleting any copy a client sent. This
// module asks the host rather than deciding again: a second reading of the org a
// token names is a second predicate, and every one this module carried disagreed
// with the boundary's somewhere — an admin-org application, an admin-org person's
// API key and an org spelled "admin " were each SuperAdmin here and nowhere else.
//
// hanzo-inc/cloud installs principal.Super. A process with no host — standalone
// aid, a raw ZAP door — has no verdict and therefore no SuperAdmin: that is
// deliberate, and it fails closed.
func SetSuper(f func(*zip.Ctx) bool) {
	if f == nil {
		super.Store(nil)
		return
	}
	super.Store(&f)
}

// Super is the host's verdict on c, false when no host stated one.
func Super(c *zip.Ctx) bool {
	if c == nil {
		return false
	}
	f := super.Load()
	return f != nil && (*f)(c)
}

// Verify is ParseAndValidateJWT for the bearer an HTTP request presented: the same
// verification, and the claims carry the host's SuperAdmin verdict on that
// request (User.Sudo). It is the one way a request's token becomes a principal
// here, so the verdict is stated at one place and read everywhere through
// util.IsSuperAdmin. A token verified with no request behind it (c nil — a raw
// ZAP door) carries none.
func Verify(c *zip.Ctx, token string) (*iam.Claims, error) {
	claims, err := ParseAndValidateJWT(token)
	if err != nil {
		return nil, err
	}
	claims.User.Sudo = Super(c)
	return claims, nil
}
