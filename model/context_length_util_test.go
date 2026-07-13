// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2025 The OpenAgent Authors. All Rights Reserved.
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

package model

import "testing"

// The contract is two lines long: whatever config declares, else the floor.
// The old table dispatched on substrings of the model name and asserted a
// hand-maintained number per model; both the table and those assertions were
// wrong for every model shipped after they were written (deepseek-v4-pro fell
// through every branch to 16384 and 402'd real requests against a model DO
// serves with an 87K window). Nothing here may re-introduce a name heuristic.

func TestGetContextLength_ConfigIsSourceOfTruth(t *testing.T) {
	t.Cleanup(func() { SetContextWindowResolver(nil) })

	SetContextWindowResolver(func(model string) int {
		switch model {
		case "glm-5.2":
			return 262144
		case "deepseek-v4-pro":
			return 87040
		}
		return 0 // undeclared
	})

	if got := GetContextLength("glm-5.2"); got != 262144 {
		t.Errorf("glm-5.2: want the declared 262144, got %d", got)
	}
	if got := GetContextLength("deepseek-v4-pro"); got != 87040 {
		t.Errorf("deepseek-v4-pro: want the declared 87040, got %d", got)
	}
	// A model the config does not declare must not be guessed at from its
	// name — it gets the floor.
	if got := GetContextLength("some-model-shipped-tomorrow"); got != DefaultContextLength {
		t.Errorf("undeclared model: want the floor %d, got %d", DefaultContextLength, got)
	}
}

func TestGetContextLength_FloorWithoutResolver(t *testing.T) {
	t.Cleanup(func() { SetContextWindowResolver(nil) })
	SetContextWindowResolver(nil)

	// No resolver installed (standalone / tests): every model gets the floor,
	// never the old 16384 that silently truncated long prompts.
	for _, m := range []string{"glm-5.2", "deepseek-v4-pro", "gpt-4o", ""} {
		if got := GetContextLength(m); got != DefaultContextLength {
			t.Errorf("%q: want floor %d, got %d", m, DefaultContextLength, got)
		}
	}
	if DefaultContextLength < 131072 {
		t.Errorf("the floor must stay >=128K: every served model is at least that, and a lower floor 402s real prompts (got %d)", DefaultContextLength)
	}
}
