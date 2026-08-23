// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"fmt"
	"sync"
	"testing"
)

// Both doors share the file cache, so concurrent readers and writers are the
// ordinary case. This holds the map's guard honest: a Go map without one is a
// runtime fatal error under concurrency, which no recover() can answer for.
func TestTheFileCacheIsReachedFromBothDoorsAtOnce(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			prefix := fmt.Sprintf("prefix-%d", i%8)
			rememberPath(prefix, fmt.Sprintf("/cache/%d", i))
			if _, ok := cachedPath(prefix); !ok {
				t.Errorf("%s was remembered and then not found", prefix)
			}
		}(i)
	}
	wg.Wait()

	for i := 0; i < 8; i++ {
		if _, ok := cachedPath(fmt.Sprintf("prefix-%d", i)); !ok {
			t.Errorf("prefix-%d is missing after the writes settled", i)
		}
	}
}
