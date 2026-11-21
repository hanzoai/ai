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
)

// RoutingChanged is a routing version applied to a family: every replica serves the
// newest version of Family, which is Version or later.
type RoutingChanged struct {
	Family  string
	Version int64
}

// routingPublisher puts an applied routing version on the host's bus; nil publishes
// nothing.
var routingPublisher atomic.Pointer[func(context.Context, RoutingChanged)]

// SetRoutingPublisher installs what puts an applied routing version on the host's bus
// (nil clears it).
func SetRoutingPublisher(f func(context.Context, RoutingChanged)) {
	if f == nil {
		routingPublisher.Store(nil)
		return
	}
	routingPublisher.Store(&f)
}

// PublishRouting hands an applied routing version to the host's bus.
func PublishRouting(ctx context.Context, c RoutingChanged) {
	if f := routingPublisher.Load(); f != nil {
		(*f)(ctx, c)
	}
}
