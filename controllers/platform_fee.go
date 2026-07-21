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

package controllers

import (
	"github.com/hanzoai/ai/object"
	iam "github.com/hanzoai/iam-v1"
)

// globalProviderOwner is the sentinel Owner of the built-in (Hanzo-served)
// providers seeded by object.initLLMProviders. A resolved provider owned by this
// sentinel is a Hanzo account (bought via Hanzo credits); a provider owned by the
// caller's own org is a customer-connected BYO account. Confirmed against
// object/provider_default.go (GetModelProviderByNameForOrg falls back to
// getProvider("admin", name) for the global built-in).
const globalProviderOwner = "admin"

// platformFeeCents returns the Hanzo platform surcharge, in integer cents, for a
// completion. BYO (a customer-connected third-party account executed the call) →
// 1% of the provider list-price cost, rounded UP so a non-zero cost never yields a
// zero fee. Buying via Hanzo credits (byo=false) → 0 (no surcharge). Pure/total.
func platformFeeCents(costCents int64, byo bool) int64 {
	if !byo || costCents <= 0 {
		return 0
	}
	return (costCents + 99) / 100
}

// providerBYO classifies a resolved (provider, billed-user) pair for metering.
// BYO is true only when the resolved provider row is owned by the caller's OWN
// org — i.e. the org connected its own third-party account (GetModelProvider
// ByNameForOrg returned the org's override) rather than being served by the
// global Hanzo built-in. account is the attribution label persisted on the usage
// record: "<owner>/<name>" for a BYO account, else "hanzo". Pure/total; a nil
// provider or nil user is Hanzo-served.
func providerBYO(provider *object.Provider, user *iam.User) (byo bool, account string) {
	if provider != nil && user != nil &&
		provider.Owner != "" && provider.Owner != globalProviderOwner &&
		provider.Owner == user.Owner {
		return true, provider.Owner + "/" + provider.Name
	}
	return false, "hanzo"
}
