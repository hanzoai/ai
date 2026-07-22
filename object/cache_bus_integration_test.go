package object

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redis "github.com/hanzoai/kv-go/v9"
)

// TestCacheBus_CrossPodInvalidation proves the FULL pub/sub loop end-to-end against
// a real Redis-protocol broker (miniredis, in-process): a DIFFERENT pod's write,
// published to the invalidation channel, reaches THIS pod's subscriber and flushes
// its local org-settings cache — the sub-second cross-pod convergence KV_URL buys.
func TestCacheBus_CrossPodInvalidation(t *testing.T) {
	mr := miniredis.RunT(t) // in-process Redis w/ pub-sub; auto-closed by t.Cleanup

	// Bring up THIS pod's bus (subscriber) pointed at the broker.
	t.Setenv("KV_URL", "redis://"+mr.Addr())
	InitCacheBus()
	t.Cleanup(func() {
		cacheBusMu.Lock()
		if cacheBusClient != nil {
			_ = cacheBusClient.Close()
			cacheBusClient = nil
		}
		cacheBusMu.Unlock()
	})

	// Seed this pod's cache as if it had served a read.
	invalidateOrgSettingsCacheLocal()
	orgSettingsCacheMu.Lock()
	orgSettingsCache["acme"] = &orgSettingsCacheEntry{settings: &OrgSettings{Owner: "acme"}, fetchedAt: time.Now()}
	orgSettingsCacheMu.Unlock()

	// A SEPARATE pod publishes an org_settings write. Retry until it lands — pub/sub
	// is lossy until this pod's subscriber has attached — or time out. A live pod
	// subscribes at boot, long before any write, so it needs no retry in production.
	pub := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer pub.Close()

	deadline := time.Now().Add(5 * time.Second)
	for {
		_ = pub.Publish(context.Background(), cacheBusChannel, "org_settings").Err()
		orgSettingsCacheMu.RLock()
		n := len(orgSettingsCache)
		orgSettingsCacheMu.RUnlock()
		if n == 0 {
			return // subscriber received the broadcast and flushed — loop proven
		}
		if time.Now().After(deadline) {
			t.Fatal("cross-pod broadcast did not flush the local cache within 5s")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
