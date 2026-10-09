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

package routers

import (
	"github.com/hanzoai/ai/object"
	"github.com/zap-proto/zip"
)

// CallerFilter puts the caller a program in this process stated on its request
// (ai.For) onto the request's context, where the gate, the tenant attribution and
// the usage record read it. A request from the network states none.
func CallerFilter(c *zip.Ctx) error {
	if who, ok := object.RequestCaller(c.Fiber().RequestCtx()); ok {
		c.SetContext(object.WithCaller(c.Context(), who))
	}
	return c.Continue()
}
