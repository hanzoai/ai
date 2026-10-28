package routers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/zap-proto/zip"
)

func IdempotencyFilter(c *zip.Ctx) error {
	if !isMutatingMethod(c.Method()) {
		return c.Continue()
	}

	key := c.Header("Idempotency-Key")
	if key == "" {
		return c.Continue()
	}

	org := GetTenantOrgID(c)
	if org == "" {
		org = c.Org()
	}
	if org == "" {
		return c.Continue()
	}

	body := c.Body()
	if isStreamingRequest(c, body) {
		return c.Continue()
	}

	fullKey := org + ":" + key
	fingerprint := requestFingerprint(c.Method(), c.Path(), c.Fiber().Request().URI().QueryString(), body)

	entry, isNew := idemStore.getOrSet(fullKey, fingerprint)

	if !isNew {
		if !idemStore.waitDone(fullKey) {
			c.Status(http.StatusGatewayTimeout)
			return nil
		}
		entry = idemStore.get(fullKey)
		if entry == nil {
			return c.Continue()
		}
		if entry.fingerprint != fingerprint {
			c.Status(http.StatusConflict)
			c.SetHeader("Content-Type", "application/json")
			c.Fiber().Response().SetBody([]byte(`{"error":"idempotency key reused with a different request"}`))
			return nil
		}
		if !entry.stored {
			return c.Continue()
		}
		c.Status(entry.status)
		if entry.contentType != "" {
			c.SetHeader("Content-Type", entry.contentType)
		}
		c.Fiber().Response().SetBody(entry.body)
		return nil
	}

	defer func() {
		if r := recover(); r != nil {
			idemStore.delete(fullKey)
			panic(r)
		}
	}()

	served := c.Continue()

	if served != nil {
		idemStore.delete(fullKey)
		return served
	}

	status := c.Fiber().Response().StatusCode()
	if status >= http.StatusInternalServerError {
		idemStore.delete(fullKey)
		return served
	}
	respBody := c.Fiber().Response().Body()
	bodyCopy := make([]byte, len(respBody))
	copy(bodyCopy, respBody)
	idemStore.complete(fullKey, status, string(c.Fiber().Response().Header.Peek("Content-Type")), bodyCopy)
	return served
}

func isMutatingMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

func requestFingerprint(method, path string, query []byte, body []byte) string {
	h := sha256.New()
	h.Write([]byte(method))
	h.Write([]byte(path))
	h.Write([]byte("?"))
	h.Write(query)
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

type idemBody struct {
	Stream bool `json:"stream"`
}

func isStreamingRequest(c *zip.Ctx, body []byte) bool {
	if strings.Contains(strings.ToLower(c.Header("Accept")), "text/event-stream") {
		return true
	}
	if v := c.Fiber().Query("stream"); v == "true" || v == "1" {
		return true
	}
	var b idemBody
	if err := json.Unmarshal(body, &b); err != nil {
		return false
	}
	return b.Stream
}
