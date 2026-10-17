// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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

package object

import (
	"context"
	"io"
	"sync/atomic"
	"time"
)

// Assets is where a file a model made is kept: the host's object store (Hanzo
// S3). The host holds the store's credential; this module names the object.
type Assets interface {
	// Put stores size bytes of r (size -1 when unknown) under key.
	Put(ctx context.Context, key, contentType string, r io.Reader, size int64) error
	// URL is an address that serves key without a credential until expires.
	URL(ctx context.Context, key string, ttl time.Duration) (url string, expires time.Time, err error)
}

var assets atomic.Pointer[Assets]

// SetAssets installs the host's object store (nil clears it).
func SetAssets(a Assets) {
	if a == nil {
		assets.Store(nil)
		return
	}
	assets.Store(&a)
}

// AssetStore is the host's object store, or nil when the host installed none.
func AssetStore() Assets {
	if a := assets.Load(); a != nil {
		return *a
	}
	return nil
}
