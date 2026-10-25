package routers

import (
	"container/list"
	"sync"
	"time"
)

const (
	idemTTL          = 10 * time.Minute
	idemWaitTimeout  = 10 * time.Second
	idemPollInterval = 50 * time.Millisecond
	idemMaxBytes     = 2 * 1024 * 1024 * 1024 // 2 GB
	idemMaxEntrySize = 512 * 1024             // 512 KB per entry
)

type idemState string

const (
	idemProcessing idemState = "processing"
	idemDone       idemState = "done"
)

type idemEntry struct {
	key         string
	fingerprint string
	status      int
	contentType string
	body        []byte
	stored      bool
	state       idemState
	createdAt   time.Time
	element     *list.Element
}

type idemCache struct {
	mu      sync.Mutex
	entries map[string]*idemEntry
	lru     *list.List
	bytes   int64
}

func newIdemCache() *idemCache {
	return &idemCache{
		entries: make(map[string]*idemEntry),
		lru:     list.New(),
	}
}

var idemStore = newIdemCache()

// getOrSet atomically inserts a processing slot. Returns the entry and true
// when this caller owns it (isNew). A concurrent caller gets the existing
// entry with isNew=false.
func (c *idemCache) getOrSet(key, fingerprint string) (*idemEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if e, ok := c.entries[key]; ok {
		if time.Since(e.createdAt) > idemTTL {
			c.removeLocked(e)
		} else {
			c.touchLocked(e)
			return e, false
		}
	}

	e := &idemEntry{
		key:         key,
		fingerprint: fingerprint,
		state:       idemProcessing,
		createdAt:   time.Now(),
	}
	e.element = c.lru.PushFront(e)
	c.entries[key] = e
	return e, true
}

func (c *idemCache) complete(key string, status int, contentType string, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.entries[key]
	if !ok || e.state != idemProcessing {
		return
	}
	if len(body) > idemMaxEntrySize {
		e.status = status
		e.state = idemDone
		return
	}
	e.status = status
	e.contentType = contentType
	e.body = body
	e.stored = true
	e.state = idemDone
	c.bytes += int64(len(body))
	c.evictLocked()
}

func (c *idemCache) delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok {
		c.removeLocked(e)
	}
}

func (c *idemCache) get(key string) *idemEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return nil
	}
	if time.Since(e.createdAt) > idemTTL {
		c.removeLocked(e)
		return nil
	}
	c.touchLocked(e)
	return e
}

func (c *idemCache) waitDone(key string) bool {
	deadline := time.Now().Add(idemWaitTimeout)
	for time.Now().Before(deadline) {
		time.Sleep(idemPollInterval)
		e := c.get(key)
		if e == nil {
			return false
		}
		if e.state == idemDone {
			return true
		}
	}
	return false
}

func (c *idemCache) touchLocked(e *idemEntry) {
	if e.element != nil {
		c.lru.MoveToFront(e.element)
	}
}

func (c *idemCache) removeLocked(e *idemEntry) {
	if e.element != nil {
		c.lru.Remove(e.element)
		e.element = nil
	}
	delete(c.entries, e.key)
	c.bytes -= int64(len(e.body))
	if c.bytes < 0 {
		c.bytes = 0
	}
}

func (c *idemCache) evictLocked() {
	for c.bytes > idemMaxBytes && c.lru.Len() > 0 {
		back := c.lru.Back()
		if back == nil {
			break
		}
		e := back.Value.(*idemEntry)
		c.removeLocked(e)
	}
}

// resetIdemStore swaps in a fresh cache. Test-only.
func resetIdemStore() {
	idemStore = newIdemCache()
}
