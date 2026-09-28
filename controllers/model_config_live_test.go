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
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// A refresh that fails at boot is tried again in seconds, not after the TTL: the pod's
// first fetch runs before the service it asks is answering, and until a fetch lands
// every model without a local row bills at the default price.
func TestAFailedPricingFetchIsRetriedSoon(t *testing.T) {
	var asks atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if asks.Add(1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"models":[{"name":"table-only","pricing":{"input":0.5,"output":2}}]}`))
	}))
	defer srv.Close()

	prev := pricingRetry
	pricingRetry = 10 * time.Millisecond
	defer func() { pricingRetry = prev }()

	mc := &ModelConfig{
		routes:     map[string]modelRoute{},
		pricing:    map[string]modelPrice{},
		defaults:   modelPrice{InputPerMillion: 1, OutputPerMillion: 4},
		pricingURL: srv.URL,
		pricingTTL: time.Hour,
		stopCh:     make(chan struct{}),
	}
	go mc.backgroundRefresh()
	defer close(mc.stopCh)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if p, ok := mc.GetPriceOK("table-only"); ok {
			if p.InputPerMillion != 0.5 || p.OutputPerMillion != 2 {
				t.Fatalf("price = %+v, want the table's 0.5/2", p)
			}
			if n := asks.Load(); n != 3 {
				t.Fatalf("asked %d times, want 3: two failures, then the table", n)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the table never loaded after %d asks", asks.Load())
}
