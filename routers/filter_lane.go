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

package routers

import (
	"github.com/hanzoai/ai/controllers"
	"github.com/zap-proto/zip"
)

// LaneFilter says on the response that a request the usage policy governs and the
// gate did not seat (controllers.Seat, decided once in BalanceGateFilter) is on the
// free lane (controllers.Lane). It runs after the gate, so a request the gate refused
// never reaches it.
func LaneFilter(c *zip.Ctx) error {
	if controllers.Entitled(c.Path()) {
		controllers.Lane(c)
	}
	return c.Continue()
}
