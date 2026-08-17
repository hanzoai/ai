// Copyright 2023-2025 Hanzo AI Inc.. All Rights Reserved.
// Portions Copyright 2024 The OpenAgent Authors. All Rights Reserved.
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
	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
	"github.com/zap-proto/zip"
)

// unrecorded are the paths that carry no principal worth attributing: signing in
// happens before there is one, and assets are static.
var unrecorded = map[string]struct{}{
	"/v1/ai/signin": {},
	"/v1/ai/assets": {},
}

// Record attributes each request to the principal that made it, and writes the row
// after the handler has answered.
//
// It was TWO filters passing a username between them through a request parameter,
// because a before-the-router filter and an after-the-handler filter have no other
// way to share. One middleware holds it in a local across the handler: nothing to
// name, nothing to look up, nothing to lose.
func Record(c *zip.Ctx) error {
	if _, skip := unrecorded[c.Path()]; skip {
		return c.Continue()
	}
	userId := getUsername(c)

	served := c.Continue()

	// Ask before composing. NewRecord geo-locates the client IP and AddRecord
	// queries the blockchain providers, and under logPostOnly a GET is discarded
	// after both — so every read paid for a row nothing kept.
	if !object.Recorded(c.Method()) {
		return served
	}
	record, err := object.NewRecord(c)
	if err != nil {
		log.Error("Record: %s", err.Error())
		return served
	}
	if userId != "" {
		organization, user, err := util.GetOwnerAndNameFromIdWithError(userId)
		if err != nil {
			// Logged, never fatal. This runs after the request has been answered, so
			// a panic here would turn a served response into a 500 over an
			// attribution string nobody reads in the moment.
			log.Error("Record: cannot attribute %q: %s", userId, err.Error())
			return served
		}
		record.Organization, record.User = organization, user
	}
	object.AddRecord(record, "en")
	return served
}
