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

package ai

import (
	"time"

	"github.com/hanzoai/ai/routers"
	"github.com/hanzoai/ai/web"
)

// wireTestSessions binds an in-memory session store to the router, matching the
// runtime's session wiring so the route tests exercise the real session path.
func wireTestSessions() {
	if web.Sessions == nil {
		web.Sessions = web.NewMemorySessions("cloud_session_id", time.Hour)
	}
	routers.App.UseSessions(web.Sessions)
}
