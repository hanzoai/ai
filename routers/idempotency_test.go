package routers

import (
	"bytes"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

func newIdemApp(t *testing.T, handler func(c *zip.Ctx) error) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Use(zip.H(IdempotencyFilter))
	app.Raw(zip.MethodAll, "/*", handler)
	return app
}

func idemDo(t *testing.T, app *zip.App, key, org, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest("POST", "/x", bytes.NewReader([]byte(body)))
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode, string(b)
}

func TestIdempotencyFirstRequest(t *testing.T) {
	resetIdemStore()
	calls := 0
	app := newIdemApp(t, func(c *zip.Ctx) error {
		calls++
		c.Status(201)
		c.Fiber().Response().SetBody([]byte("created"))
		c.SetHeader("Content-Type", "text/plain")
		return nil
	})
	status, body := idemDo(t, app, "first", "org1", `{"a":1}`)
	if status != 201 || body != "created" {
		t.Fatalf("first = %d %q", status, body)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestIdempotencyReplayStoredResponse(t *testing.T) {
	resetIdemStore()
	calls := 0
	app := newIdemApp(t, func(c *zip.Ctx) error {
		calls++
		c.Status(201)
		c.Fiber().Response().SetBody([]byte("created"))
		c.SetHeader("Content-Type", "text/plain")
		return nil
	})
	idemDo(t, app, "replay", "org1", `{"a":1}`)
	status, body := idemDo(t, app, "replay", "org1", `{"a":1}`)
	if status != 201 || body != "created" {
		t.Fatalf("replay = %d %q", status, body)
	}
	if calls != 1 {
		t.Fatalf("handler re-executed: calls = %d", calls)
	}
}

func TestIdempotencyConflictDifferentBody(t *testing.T) {
	resetIdemStore()
	calls := 0
	app := newIdemApp(t, func(c *zip.Ctx) error {
		calls++
		c.Status(200)
		c.Fiber().Response().SetBody([]byte("ok"))
		return nil
	})
	if status, _ := idemDo(t, app, "conflict", "org1", `{"a":1}`); status != 200 {
		t.Fatalf("first = %d", status)
	}
	status, _ := idemDo(t, app, "conflict", "org1", `{"a":2}`)
	if status != 409 {
		t.Fatalf("second = %d, want 409", status)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestIdempotencyConcurrentInFlight(t *testing.T) {
	resetIdemStore()
	var mu sync.Mutex
	calls := 0
	app := newIdemApp(t, func(c *zip.Ctx) error {
		mu.Lock()
		calls++
		mu.Unlock()
		c.Status(200)
		c.Fiber().Response().SetBody([]byte("ok"))
		return nil
	})
	var wg sync.WaitGroup
	statuses := make([]int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, _ := idemDo(t, app, "conc", "org1", `{"a":1}`)
			statuses[i] = s
		}(i)
	}
	wg.Wait()
	for _, s := range statuses {
		if s != 200 {
			t.Fatalf("statuses = %v", statuses)
		}
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestIdempotencyExpiry(t *testing.T) {
	resetIdemStore()
	cache := idemStore
	_, isNew := cache.getOrSet("expiry", "fp")
	if !isNew {
		t.Fatal("expected new entry")
	}
	cache.complete("expiry", 200, "text/plain", []byte("hi"))
	entry := cache.get("expiry")
	if entry == nil {
		t.Fatal("entry missing")
	}
	entry.createdAt = time.Now().Add(-idemTTL - time.Second)
	if got := cache.get("expiry"); got != nil {
		t.Fatalf("expected expired entry to be removed, got %+v", got)
	}
	if _, isNew := cache.getOrSet("expiry", "fp"); !isNew {
		t.Fatal("expected new entry after expiry")
	}
}

func TestIdempotencyStreamingBypass(t *testing.T) {
	resetIdemStore()
	calls := 0
	app := newIdemApp(t, func(c *zip.Ctx) error {
		calls++
		c.Status(200)
		c.Fiber().Response().SetBody([]byte("ok"))
		return nil
	})
	if s, _ := idemDo(t, app, "stream", "org1", `{"stream":true}`); s != 200 {
		t.Fatalf("first = %d", s)
	}
	if s, _ := idemDo(t, app, "stream", "org1", `{"stream":true}`); s != 200 {
		t.Fatalf("second = %d", s)
	}
	if calls != 2 {
		t.Fatalf("streaming not bypassed: calls = %d", calls)
	}
}

func TestIdempotencyOversizedResponse(t *testing.T) {
	resetIdemStore()
	calls := 0
	app := newIdemApp(t, func(c *zip.Ctx) error {
		calls++
		c.Status(200)
		big := strings.Repeat("a", idemMaxEntrySize+10)
		c.Fiber().Response().SetBody([]byte(big))
		c.SetHeader("Content-Type", "text/plain")
		return nil
	})
	status, body := idemDo(t, app, "big", "org1", `{"a":1}`)
	if status != 200 || len(body) != idemMaxEntrySize+10 {
		t.Fatalf("first = %d len=%d", status, len(body))
	}
	status2, _ := idemDo(t, app, "big", "org1", `{"a":1}`)
	if status2 != 409 {
		t.Fatalf("replay = %d, want 409", status2)
	}
	if calls != 1 {
		t.Fatalf("handler re-executed: calls = %d", calls)
	}
}
