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
	"github.com/hanzoai/ai/web"
)

// unrecorded are the paths that carry no principal worth attributing: signing in
// happens before there is one, and assets are static.
var unrecorded = map[string]struct{}{
	"/v1/ai/signin": {},
	"/v1/ai/assets": {},
}

func RecordMessage(ctx *web.Context) {
	if _, skip := unrecorded[ctx.Request.URL.Path]; skip {
		return
	}

	userId := getUsername(ctx)
	ctx.Input.SetParam("recordUserId", userId)
}

func AfterRecordMessage(ctx *web.Context) {
	// Ask before composing. NewRecord geo-locates the client IP and AddRecord
	// queries the blockchain providers, and under logPostOnly a GET is discarded
	// after both — so every read paid for a row nothing kept.
	if !object.Recorded(ctx.Request.Method) {
		return
	}

	record, err := object.NewRecord(ctx)
	if err != nil {
		log.Error("AfterRecordMessage() error: %s", err.Error())
		return
	}

	userId := ctx.Input.Params()["recordUserId"]
	if userId != "" {
		organization, user, err := util.GetOwnerAndNameFromIdWithError(userId)
		if err != nil {
			panic(err)
		}
		record.Organization, record.User = organization, user
	}

	object.AddRecord(record, "en")
}
