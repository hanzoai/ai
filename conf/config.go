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

package conf

import (
	"os"
	"strconv"
	"strings"
	"sync"
)

// config holds the flat key/value pairs parsed from app.conf. Reads through
// GetConfigString are env-first, so this file store is the fallback.
type config struct {
	mu   sync.RWMutex
	data map[string]string
}

// AppConfig is the process configuration, populated from conf/app.conf on
// startup (when present) and overridable via Set. The embedded runtime ships
// without app.conf and runs env-only.
var AppConfig = &config{data: map[string]string{}}

func init() {
	// Best-effort load of the local config; absence is normal (env-only).
	_ = LoadAppConfig("ini", "conf/app.conf")
}

// LoadAppConfig parses a flat "key = value" ini file into AppConfig: it skips
// blank lines, ";"/"#" comments and "[section]" headers, trims whitespace and
// strips surrounding double quotes. The format argument is accepted for call
// compatibility; only ini is supported.
func LoadAppConfig(format, path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	m := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"`)
		m[k] = v
	}
	AppConfig.mu.Lock()
	AppConfig.data = m
	AppConfig.mu.Unlock()
	return nil
}

// String returns the value for key, or "" when unset.
func (c *config) String(key string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.data[key]
}

// Set overrides a key (used by tests to force a value).
func (c *config) Set(key, value string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[key] = value
	return nil
}

// Bool parses the value for key as a bool.
func (c *config) Bool(key string) (bool, error) {
	return strconv.ParseBool(c.String(key))
}

// DefaultBool returns the value for key parsed as a bool, or def when unset or
// unparseable.
func (c *config) DefaultBool(key string, def bool) bool {
	v := c.String(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

// DefaultInt returns the value for key parsed as an int, or def when unset or
// unparseable.
func (c *config) DefaultInt(key string, def int) int {
	v := c.String(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// DefaultString returns the value for key, or def when unset.
func (c *config) DefaultString(key, def string) string {
	if v := c.String(key); v != "" {
		return v
	}
	return def
}
