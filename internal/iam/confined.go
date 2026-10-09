// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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

package iam

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/golang-jwt/jwt/v4"
)

// Inference is the scope of a token IAM DELEGATED for model calls (iam
// schema.Inference, iam LLM.md "Delegation"): a person's credential, in one org,
// handed to a process the host runs for them and does not trust. IAM signs one
// with a key it never publishes, so ParseJwtToken — which reads the public JWKS —
// cannot verify it and must not try. The host that serves model calls holds that
// key and verifies it (object.SetConfinedVerifier).
const Inference = "ai:inference"

// Confined reports whether token's payload names Inference in its scope.
//
// It is read UNVERIFIED, and that is its whole use: choosing which verifier a
// token goes to. It never admits anything. A forger who writes the scope into a
// payload sends the token to the stricter verifier, the one that refuses every
// key but the delegation key.
func Confined(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var p struct {
		Scope string `json:"scope"`
	}
	return json.Unmarshal(raw, &p) == nil && slices.Contains(strings.Fields(p.Scope), Inference)
}

// ClaimsOf reads the claims of a token THE HOST HAS ALREADY VERIFIED, exactly as
// ParseJwtToken reads a verified token's: the same decoder, and the same two facts
// stamped after it (the principal's class and its home org). It checks nothing —
// calling it on a token nobody verified would be believing a forger — so its one
// caller is the confined branch of object.ParseAndValidateJWT, after the host's
// verifier said yes to these exact bytes.
func ClaimsOf(token string) (*Claims, error) {
	claims := &Claims{}
	if _, _, err := jwt.NewParser().ParseUnverified(token, claims); err != nil {
		return nil, err
	}
	if claims.Subject == "" {
		return nil, errors.New("iam: a confined token names no subject")
	}
	claims.typeMachine()
	claims.homeOrg()
	return claims, nil
}
