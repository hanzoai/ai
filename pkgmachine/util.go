// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2025 The OpenAgent Authors. All Rights Reserved.
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

package pkgmachine

import "time"

func getLocalTimestamp(input string) string {
	if input == "" {
		return ""
	}

	// Providers report timestamps in different shapes: RFC3339 with a zone
	// offset or fractional seconds (GCP), and a minute-precision UTC form
	// (Aliyun). Try each known layout and normalize to local RFC3339; an
	// unparseable value degrades to empty rather than panicking.
	layouts := []string{
		time.RFC3339,
		"2006-01-02T15:04Z",
	}
	for _, layout := range layouts {
		if utcTime, err := time.Parse(layout, input); err == nil {
			return utcTime.Local().Format(time.RFC3339)
		}
	}

	return ""
}
