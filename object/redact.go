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

import (
	"reflect"
	"strings"

	iam "github.com/hanzoai/iam"
)

// exposableUserFields is the ALLOWLIST of iam.User json tags (string and []string
// fields) that are safe to return on a default user read (e.g. /v1/get-account).
// It is intentionally a projection, NOT a denylist: a string/[]string field whose
// tag is NOT listed here is zeroed. This is fail-secure — a NEW iam.User secret
// field (a token, key, or hash added upstream) is never exposed until it is
// explicitly allowlisted, closing the denylist gap where any new credential field
// would silently leak. The set covers identity, profile, contact, locale, status,
// balance, and the fields the cloud account UI consumes (incl. createdIp).
// Deliberately EXCLUDED (→ zeroed): password*, access*/token* credentials,
// totpSecret/recoveryCodes, hash/preHash/externalId, idCard*/realName/ldap,
// OAuth provider link ids (github…web3onboard), custom1..10, invitation*, and IP
// allowlists — none are needed by the cloud account surface.
var exposableUserFields = map[string]struct{}{
	"owner": {}, "name": {}, "createdTime": {}, "updatedTime": {}, "deletedTime": {},
	"id": {}, "type": {}, "displayName": {}, "firstName": {}, "lastName": {},
	"avatar": {}, "avatarType": {}, "permanentAvatar": {},
	"email": {}, "phone": {}, "countryCode": {}, "region": {}, "location": {}, "address": {},
	"affiliation": {}, "title": {}, "homepage": {}, "bio": {}, "tag": {},
	"language": {}, "gender": {}, "birthday": {}, "education": {},
	"currency": {}, "balanceCurrency": {}, "signupApplication": {},
	"createdIp": {}, "lastSigninTime": {}, "lastSigninIp": {},
	"preferredMfaType": {}, "groups": {},
}

// redactNonExposableStrings zeros every string and []string field of u whose json
// tag is NOT in exposableUserFields (the allowlist projection). Non-string fields
// (bools, ints, floats, maps, and struct/pointer slices) are untouched here;
// credential-bearing struct slices are cleared explicitly by RedactUserSecrets.
func redactNonExposableStrings(u *iam.User) {
	v := reflect.ValueOf(u).Elem()
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" { // unexported field — skip
			continue
		}
		tag := strings.Split(f.Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		if _, ok := exposableUserFields[tag]; ok {
			continue
		}
		fv := v.Field(i)
		if !fv.CanSet() {
			continue
		}
		switch {
		case fv.Kind() == reflect.String:
			fv.SetString("")
		case fv.Kind() == reflect.Slice && fv.Type().Elem().Kind() == reflect.String:
			fv.Set(reflect.Zero(fv.Type()))
		}
	}
}

// RedactUserSecrets scrubs every credential-bearing field on an IAM user before it
// is serialized to a client. This is the ONE place cloud-api scrubs a user; the
// default read path (e.g. /v1/get-account) must call it so a browser never
// receives the password hash/salt, MFA seed, recovery codes, access/refresh
// tokens, or any other credential. It is an ALLOWLIST projection over string and
// []string fields (redactNonExposableStrings) so a future secret field is
// fail-secure, plus explicit clearing of the credential-bearing STRUCT slices the
// string projection cannot reach.
func RedactUserSecrets(u *iam.User) {
	if u == nil {
		return
	}

	// Allowlist projection: zero every string/[]string field not explicitly
	// exposable. This covers password*, *Secret, *Key, *Token, totpSecret,
	// recoveryCodes, AND any new unknown string credential (fail-secure).
	redactNonExposableStrings(u)

	// Credential-bearing STRUCT slices the string projection cannot reach.
	u.RecoveryCodes = nil // also covered by the projection; explicit for clarity.
	for _, m := range u.MultiFactorAuths {
		if m == nil {
			continue
		}
		m.Secret = ""
		m.RecoveryCodes = nil
	}
	u.MfaAccounts = nil     // MfaAccount carries a TOTP secret key.
	u.ManagedAccounts = nil // ManagedAccount carries a password.
	u.MfaItems = nil        // MFA enrollment items.
	u.FaceIds = nil         // biometric face data.
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
