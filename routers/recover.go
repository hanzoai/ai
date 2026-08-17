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

package routers

import (
	"net/http"
	"runtime/debug"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/ai/log"
)

// Recovered turns a panic into a 500, so one request cannot take anything else with
// it.
//
// It is FIRST in the chain, which is deliberate: it has to cover the filters as well
// as the handlers. A gate that panics while deciding whether to admit a request is
// the worst place to lose the process, because it happens before anything has been
// served.
//
// This is not new behaviour, it is behaviour that had to be moved. The router this
// service used to run recovered in two places of its own — once around the filter
// chain and once around handler dispatch — so a nil dereference anywhere in ~360
// handlers became a 500 and the process carried on. zip installs no recovery (its
// own examples show the app adding one), and under fasthttp an unrecovered panic
// takes the connection. Registering the routes natively removed the old router and
// its recover with it; this is the recover, in one place, where the chain is built.
//
// The body is the house envelope and says nothing else. A panic is ours, not the
// caller's: the stack goes to the log where an engineer reads it, and the caller gets
// a 500 they can retry rather than a description of our internals.
func Recovered(c *zip.Ctx) (err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Error("panic serving %s %s: %v\n%s", c.Method(), c.Path(), r, debug.Stack())
			err = c.JSON(http.StatusInternalServerError, Response{
				Status: "error",
				Msg:    "internal error",
			})
		}
	}()
	return c.Continue()
}
