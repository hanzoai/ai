// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2023 The OpenAgent Authors. All Rights Reserved.
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

// These are operator SCRIPTS, not tests: they assert nothing, print results
// with fmt.Printf, panic instead of failing, and need a populated database,
// live provider credentials or local fixture files this repository does not
// contain. Run one deliberately:
//
//	go test -tags ai_scripts ./<pkg>/ -run TestName -v
//
// Behind a tag because a panicking test aborts the whole test binary, so one
// missing fixture took every real test in the package down with it. Several
// also WRITE to whatever database they find, which `go test ./...` must not.
//go:build !skipCi && ai_scripts

package storage_test

import (
	"fmt"
	"testing"

	"github.com/hanzoai/ai/controllers"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/storage"
)

func TestStorage(t *testing.T) {
	object.InitConfig()
	controllers.InitAuthConfig()

	provider := "provider_storage_cloud"
	providerObj, err := storage.NewIamProvider(provider, "en")
	objects, err := providerObj.ListObjects("")
	if err != nil {
		panic(err)
	}

	for i, obj := range objects {
		fmt.Printf("[%d] %v\n", i, obj)
	}
}
