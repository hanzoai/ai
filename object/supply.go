// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package object

import (
	"context"
	"sync/atomic"
	"time"
)

// Drained is one of our accounts with a vendor that answered it cannot pay: a 402,
// or the vendor's own word for an account out of credit. Account is the NAME the
// key is declared under (OPENROUTER_API_KEY_3), or the provider row's name for a
// row with one key; never the key. Model is the model the refused call was for.
type Drained struct {
	Vendor  string
	Account string
	Model   string
	Status  int
	At      time.Time
}

// drainedPublisher puts a drained account on the host's bus; nil publishes nothing.
var drainedPublisher atomic.Pointer[func(context.Context, Drained)]

// SetDrainedPublisher installs what puts a drained account on the host's bus (nil
// clears it).
func SetDrainedPublisher(f func(context.Context, Drained)) {
	if f == nil {
		drainedPublisher.Store(nil)
		return
	}
	drainedPublisher.Store(&f)
}

// PublishDrained hands a drained account to the host's bus.
func PublishDrained(ctx context.Context, s Drained) {
	if f := drainedPublisher.Load(); f != nil {
		(*f)(ctx, s)
	}
}
