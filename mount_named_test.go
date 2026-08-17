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

package ai

import (
	"strings"
	"testing"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/ai/routers"
)

// EVERY ADDRESS IS ITS OWN ROUTE — which is what makes it describable.
//
// There used to be a hand-written "promoted" list beside a /v1/* glob: three
// addresses re-registered specifically so the host could name them, because a glob
// tells a host a prefix and nothing under it. Three operations were authenticated,
// reachable and answering in production with no generated SDK, no MCP tool, no CLI
// command, and nothing saying who served them.
//
// The list is gone because there is nothing left to promote, and this holds the
// property it was reaching for: the table names real patterns, and no glob stands in
// for them.
func TestEveryAddressIsItsOwnRoute(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true, ReadBufferSize: 32 << 10})
	routes(app)

	patterns := routers.Patterns(app)
	if len(patterns) == 0 {
		t.Fatal("the app registered no addresses at all")
	}

	// The three the old list named. They are ordinary entries now, which is the
	// point: nothing distinguishes them from the other ~190.
	for _, pattern := range []string{
		"/v1/models",
		"/v1/models/providers",
		"/v1/models/:model/access",
	} {
		if len(patterns[pattern]) == 0 {
			t.Errorf("%s is not a route; the table cannot describe what it does not register", pattern)
		}
	}

	// And no glob. One would make every address under it undescribable again.
	for pattern := range patterns {
		if strings.HasSuffix(pattern, "/*") || strings.Contains(pattern, "/*/") {
			t.Errorf("%s is a glob — the addresses under it have no description", pattern)
		}
	}
}

// The test that used to sit here installed a stub handler and asserted a promoted
// address reached the same one as the glob. Both halves are gone: there is no stub
// to install and no glob to compare against, because each address reaches its own
// controller through its own route. That is the property, and it is above.
