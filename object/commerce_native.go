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

import "context"

// The ONE native billing seam. A HOST binary that embeds this module co-resident
// with the finance ledger (hanzoai/cloud, unified binary) installs these typed hooks
// at boot; every prepaid balance read + usage debit then dispatches DIRECTLY to the
// host's in-process finance client (per-org SQLite double-entry wallet) — no HTTP, no
// socket, no serialization: in-proc ZAP, not a network hop. nil (the default, e.g.
// standalone ai) → the call falls back to the module's HTTP path against
// commerceEndpoint, unchanged. This replaces the prior http.RoundTripper transport
// seam: money is a TYPED call now, never an HTTP request the host has to re-route.

// BalanceReaderFunc returns the subject's AVAILABLE prepaid balance in CENTS within
// the org namespace. subject is the billing subject ("owner/name" for a per-user
// wallet or the org slug for a pooled wallet); namespace is the org (X-Org-Id).
type BalanceReaderFunc func(ctx context.Context, subject, namespace, currency string) (availableCents int64, err error)

// UsageRecorderFunc debits a metered usage event from the subject's wallet.
type UsageRecorderFunc func(ctx context.Context, u UsageEvent) error

// UsageEvent is one metered debit — the typed twin of the old /v1/billing/usage body.
type UsageEvent struct {
	Subject   string // billing subject (SourceId): "owner/name" or org slug
	Namespace string // org (X-Org-Id)
	Cents     int64  // amount to debit (> 0)
	Currency  string // default "usd"
	Model     string
	Provider  string
	RequestID string // idempotency key
}

var (
	balanceReader BalanceReaderFunc
	usageRecorder UsageRecorderFunc
)

// SetBalanceReader installs the host's native balance reader (nil clears it).
func SetBalanceReader(f BalanceReaderFunc) { balanceReader = f }

// SetUsageRecorder installs the host's native usage recorder (nil clears it).
func SetUsageRecorder(f UsageRecorderFunc) { usageRecorder = f }

// BalanceReader returns the installed native reader, or nil when unset (standalone).
func BalanceReader() BalanceReaderFunc { return balanceReader }

// UsageRecorder returns the installed native recorder, or nil when unset.
func UsageRecorder() UsageRecorderFunc { return usageRecorder }
