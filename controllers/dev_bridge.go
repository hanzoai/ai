// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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
	"bufio"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/beego/beego/logs"
	"github.com/gorilla/websocket"
)

const maxBridgeConnsPerUser = 5

// devBridgeUpgrader is a dedicated WebSocket upgrader for DevBridge with
// origin checking to prevent cross-site WebSocket hijacking. The global
// UpGrader in tunnel.go accepts all origins for guacamole compatibility;
// DevBridge must not share it.
var devBridgeUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true // non-browser clients (CLI, curl)
		}
		for _, allowed := range []string{
			"https://cloud.hanzo.ai",
			"https://platform.hanzo.ai",
			"http://localhost:",
			"http://127.0.0.1:",
		} {
			if strings.HasPrefix(origin, allowed) {
				return true
			}
		}
		return false
	},
}

// bridgeConns tracks active bridge connections per user key (username or IP).
// Values are *int64 counters.
var bridgeConns sync.Map

// blockedPrefixes are system directories that cwd must never resolve into.
var blockedPrefixes = []string{"/etc", "/var", "/usr", "/bin", "/sbin", "/sys", "/proc"}

// validateCwd resolves cwd to an absolute path and rejects unsafe values.
func validateCwd(raw string) (string, error) {
	if strings.Contains(raw, "..") {
		return "", fmt.Errorf("path must not contain '..'")
	}

	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", fmt.Errorf("cannot resolve path: %w", err)
	}
	abs = filepath.Clean(abs)

	// Must exist and be a directory.
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("path does not exist: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("path is not a directory")
	}

	// Allow home dirs and /tmp only.
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	inHome := strings.HasPrefix(abs, homeDir+"/") || abs == homeDir
	inTmp := strings.HasPrefix(abs, "/tmp/") || abs == "/tmp"
	if !inHome && !inTmp {
		return "", fmt.Errorf("path must be under home directory or /tmp")
	}

	// Reject system directories even if they somehow appear under allowed roots.
	for _, prefix := range blockedPrefixes {
		if strings.HasPrefix(abs, prefix+"/") || abs == prefix {
			return "", fmt.Errorf("path must not be a system directory")
		}
	}

	return abs, nil
}

// DevBridge upgrades to WebSocket and bridges JSON-RPC messages between
// the browser and a hanzo-app-server process over stdio.
//
// GET /api/dev-bridge?cwd=/path/to/project
func (c *ApiController) DevBridge() {
	c.EnableRender = false
	ctx := c.Ctx

	// --- auth: require authenticated session ---
	// DevBridge route does not match the get-/update-/add-/delete- prefixes
	// checked by the permission filter, so we must gate auth explicitly.
	user := c.GetSessionUser()
	if user == nil {
		c.ResponseError(c.T("auth:Please sign in first"))
		return
	}

	// --- cwd validation ---
	cwd := c.Input().Get("cwd")
	if cwd == "" {
		cwd = "."
	}
	safeCwd, err := validateCwd(cwd)
	if err != nil {
		logs.Error(fmt.Sprintf("DevBridge: invalid cwd %q: %s", cwd, err.Error()))
		ctx.ResponseWriter.WriteHeader(http.StatusBadRequest)
		_, _ = ctx.ResponseWriter.Write([]byte(fmt.Sprintf(`{"error":"invalid cwd: %s"}`, err.Error())))
		return
	}

	// --- per-user connection limit ---
	userKey := GetUserName(user)
	counterVal, _ := bridgeConns.LoadOrStore(userKey, new(int64))
	counter := counterVal.(*int64)
	if atomic.AddInt64(counter, 1) > maxBridgeConnsPerUser {
		atomic.AddInt64(counter, -1)
		logs.Warn(fmt.Sprintf("DevBridge: connection limit exceeded for %s", userKey))
		ctx.ResponseWriter.WriteHeader(http.StatusTooManyRequests)
		_, _ = ctx.ResponseWriter.Write([]byte(`{"error":"too many concurrent connections"}`))
		return
	}
	defer atomic.AddInt64(counter, -1)

	ws, err := devBridgeUpgrader.Upgrade(ctx.ResponseWriter, ctx.Request, nil)
	if err != nil {
		logs.Error(fmt.Sprintf("DevBridge: websocket upgrade failed: %s", err.Error()))
		return
	}
	defer ws.Close()

	cmd := exec.Command("hanzo-app-server")
	cmd.Dir = safeCwd

	stdin, err := cmd.StdinPipe()
	if err != nil {
		logs.Error(fmt.Sprintf("DevBridge: stdin pipe: %s", err.Error()))
		writeWSError(ws, "failed to create stdin pipe")
		return
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		logs.Error(fmt.Sprintf("DevBridge: stdout pipe: %s", err.Error()))
		writeWSError(ws, "failed to create stdout pipe")
		return
	}

	if err := cmd.Start(); err != nil {
		logs.Error(fmt.Sprintf("DevBridge: start app-server: %s", err.Error()))
		writeWSError(ws, "failed to start hanzo-app-server")
		return
	}

	// --- process cleanup: always kill even on panic ---
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	var wsMu sync.Mutex
	var wg sync.WaitGroup
	done := make(chan struct{})
	var doneOnce sync.Once
	closeDone := func() { doneOnce.Do(func() { close(done) }) }

	// WebSocket -> stdin: forward client JSON-RPC messages to app-server.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer stdin.Close()
		defer closeDone()
		for {
			select {
			case <-done:
				return
			default:
			}
			_, message, err := ws.ReadMessage()
			if err != nil {
				logs.Info(fmt.Sprintf("DevBridge: ws read closed: %s", err.Error()))
				return
			}
			if _, err := stdin.Write(append(message, '\n')); err != nil {
				logs.Error(fmt.Sprintf("DevBridge: stdin write: %s", err.Error()))
				return
			}
		}
	}()

	// stdout -> WebSocket: forward app-server JSON-RPC responses to client.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer closeDone()
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		for scanner.Scan() {
			select {
			case <-done:
				return
			default:
			}
			wsMu.Lock()
			err := ws.WriteMessage(websocket.TextMessage, scanner.Bytes())
			wsMu.Unlock()
			if err != nil {
				logs.Error(fmt.Sprintf("DevBridge: ws write: %s", err.Error()))
				return
			}
		}
		if err := scanner.Err(); err != nil {
			logs.Error(fmt.Sprintf("DevBridge: stdout scan: %s", err.Error()))
		}
	}()

	// --- wait with timeout to prevent goroutine leak ---
	waitCh := make(chan struct{})
	go func() {
		wg.Wait()
		close(waitCh)
	}()
	select {
	case <-waitCh:
	case <-time.After(30 * time.Second):
		logs.Warn("DevBridge: goroutine wait timed out after 30s, forcing cleanup")
		closeDone()
		_ = ws.Close()
	}
}

func writeWSError(ws *websocket.Conn, msg string) {
	_ = ws.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"error":"%s"}`, msg)))
}
