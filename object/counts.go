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
	"time"
)

// CountWindow is one ceiling a shared count charges: the key it is kept under, how
// many it admits, and how long the key outlives its first charge. The caller names
// the period in the key, so a new period is a new key and nothing resets.
type CountWindow struct {
	Key   string
	Limit int
	TTL   time.Duration
}

// QuotaFunc charges one to every window at once, and only when each is below its
// limit, on the host's shared store: every replica counts the same keys and a
// restart keeps them. It answers each window's count after the call and whether
// the call was charged.
type QuotaFunc func(ctx context.Context, windows []CountWindow) (used []int, ok bool)

// TakeFunc spends one token of key's bucket on the host's shared store: perMin a
// minute, refilled continuously.
type TakeFunc func(ctx context.Context, key string, perMin int) bool

var (
	quotaCount QuotaFunc
	takeCount  TakeFunc
)

// SetCounters installs the host's shared counts for the request-rate and quota
// ceilings (nil clears them, and the ceilings count in this process).
func SetCounters(q QuotaFunc, t TakeFunc) { quotaCount, takeCount = q, t }

// Counts reports the host's shared counts, nil when none is installed.
func Counts() (QuotaFunc, TakeFunc) { return quotaCount, takeCount }
