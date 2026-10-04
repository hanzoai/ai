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

package controllers

import (
	"context"
	"testing"

	"github.com/hanzoai/ai/object"
)

// A per-unit operation bills its unit price: three pages scraped at 1¢ each debit
// $0.03. The charge used to ride only in Cost, which nothing on the money path
// reads, so every scrape, crawl and Zen media call debited $0.
func TestAUnitPricedCallDebitsItsPrice(t *testing.T) {
	var got []object.UsageEvent
	object.SetUsageRecorder(func(_ context.Context, u object.UsageEvent) error {
		got = append(got, u)
		return nil
	})
	t.Cleanup(func() { object.SetUsageRecorder(nil) })

	recordSearchUsage(&searchAuth{Owner: "acme", UserID: "acme/ann"}, "scrape", "crawl", "success", 3, "")
	if len(got) != 1 || got[0].USD != "0.03" || got[0].Namespace != "acme" {
		t.Fatalf("a 3-page scrape debited %+v, want $0.03 on acme", got)
	}
	got = nil
	recordSearchUsage(&searchAuth{Owner: "acme", UserID: "acme/ann"}, "scrape", "crawl", "error", 3, "")
	if len(got) == 1 && got[0].USD != "0" && got[0].USD != "" {
		t.Fatalf("a failed scrape debited %q", got[0].USD)
	}
}
