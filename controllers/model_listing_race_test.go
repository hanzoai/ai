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
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	iam "github.com/hanzoai/ai/internal/iam"
)

// One build is shared by every endpoint, so the readers are concurrent HTTP handlers
// and the writer is a config reload. This holds the memo to that: run it under
// -race and neither the held slice nor the held body may be touched while another
// caller is reading it.
//
// It is here because the property is the one a memo can most easily lose and the
// one a single-threaded test cannot see at all.
func TestTheHeldCatalogueIsSafeToReadConcurrently(t *testing.T) {
	withCatalog(t, "alpha", "beta", "gamma")

	var readers, writer sync.WaitGroup
	stop := make(chan struct{})

	// The writer: reload the config under the readers. Reload re-applies in place
	// under the config's own write lock, stamping changedAt, so the memo's inputs
	// move and rebuilds happen while reads are in flight — without reassigning
	// globalModelConfig, which nothing synchronizes and which is not what is
	// under test here.
	//
	// It waits on its own group, because it runs until the readers are done and so
	// can never be waited on alongside them.
	writer.Add(1)
	go func() {
		defer writer.Done()
		for {
			select {
			case <-stop:
				return
			default:
				if err := GetModelConfig().Reload(); err != nil {
					t.Errorf("reload: %v", err)
					return
				}
				// Reload reads a file. Left unpaced it starves the readers this
				// test exists to run.
				time.Sleep(time.Millisecond)
			}
		}
	}()

	for i := 0; i < 8; i++ {
		readers.Add(1)
		// Each reader is a different signed-in caller, because a listing is
		// annotated with the READER's own standing — which is the write that a
		// shared backing array would let one caller land in another's answer.
		caller := &iam.User{Owner: "hanzo", Name: fmt.Sprintf("caller-%d", i)}
		go func() {
			defer readers.Done()
			for n := 0; n < 200; n++ {
				models := listAvailableModels()
				if len(models) == 0 {
					t.Error("listAvailableModels returned nothing")
					return
				}
				// Read every element: a shared backing array that another caller is
				// writing shows up here and nowhere else.
				for _, m := range models {
					_ = m.ID
				}

				body, err := modelListing(caller)
				if err != nil {
					t.Errorf("modelListing: %v", err)
					return
				}
				var envelope struct {
					Data []struct {
						ID string `json:"id"`
					} `json:"data"`
				}
				if err := json.Unmarshal(body, &envelope); err != nil {
					t.Errorf("held body is not valid json: %v", err)
					return
				}
				if len(envelope.Data) == 0 {
					t.Error("held body lists no models")
					return
				}
			}
		}()
	}

	readers.Wait()
	close(stop)
	writer.Wait()
}
