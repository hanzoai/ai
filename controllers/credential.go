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

package controllers

import "strings"

// Credential is the key a caller presented: Authorization: Bearer, else
// x-api-key, the header the Anthropic wire and its SDKs carry the key on. The
// router's gate and the Anthropic controller both read it here, so a request
// carrying both headers is admitted and billed on the same key.
func Credential(authorization, apiKey string) string {
	if after, ok := strings.CutPrefix(authorization, "Bearer "); ok {
		return after
	}
	return strings.TrimSpace(apiKey)
}
