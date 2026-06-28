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

package object

import iam "github.com/hanzoai/iam"

// RedactUserSecrets zeroes every credential-bearing field on an IAM user before
// it is serialized to a client. This is the ONE place cloud-api scrubs a user;
// the default read path (e.g. /v1/get-account) must call it so a browser never
// receives the password hash, salt, MFA seed, recovery codes, or any
// access/refresh token. Access keys are also scrubbed here — they are revealed
// ONLY through an explicit, separately-authorized endpoint, never a default read.
func RedactUserSecrets(u *iam.User) {
	if u == nil {
		return
	}
	// password* — never expose the hash, salt, or KDF identifier.
	u.Password = ""
	u.PasswordSalt = ""
	u.PasswordType = ""

	// *Secret / *Key — API credentials.
	u.AccessKey = ""
	u.AccessSecret = ""

	// *Token — bearer/refresh material.
	u.AccessToken = ""
	u.OriginalToken = ""
	u.OriginalRefreshToken = ""

	// MFA seeds and recovery codes.
	u.TotpSecret = ""
	u.RecoveryCodes = nil
	for _, m := range u.MultiFactorAuths {
		if m == nil {
			continue
		}
		m.Secret = ""
		m.RecoveryCodes = nil
	}
}

// RedactClaimsSecrets scrubs both the embedded user and the claims-level token
// material from an IAM claims object before it is returned to a client.
func RedactClaimsSecrets(c *iam.Claims) {
	if c == nil {
		return
	}
	RedactUserSecrets(&c.User)
	c.AccessToken = ""
}
