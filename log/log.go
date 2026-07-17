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

// Package log is the leveled logging surface for the ai runtime. Records
// emit through luxfi/log to structured stderr, where the unified cloud
// process (HIP-0106) has its ZAP/o11y pipeline collect them.
//
// Info/Warning/Warn/Error take a leading format value plus optional args:
// a string containing a format verb is used as a fmt format string; any
// other leading value, or trailing args when the string has no verb, are
// rendered and appended as " %v" fields. That formatting contract lets the
// call sites read and format exactly as before.
package log

import (
	"encoding/json"
	"fmt"
	"strings"

	luxlog "github.com/luxfi/log"
)

// Info logs at info level.
func Info(f interface{}, v ...interface{}) { luxlog.Info(format(f, v...)) }

// Warning logs at warn level.
func Warning(f interface{}, v ...interface{}) { luxlog.Warn(format(f, v...)) }

// Warn logs at warn level.
func Warn(f interface{}, v ...interface{}) { luxlog.Warn(format(f, v...)) }

// Error logs at error level.
func Error(f interface{}, v ...interface{}) { luxlog.Error(format(f, v...)) }

// SetLogger applies the process log level from a JSON config's optional
// "level" field. The runtime emits structured records to stderr for the
// platform to collect (HIP-0106); the historical adapter names ("file",
// "console") denote that single sink and carry no per-adapter wiring.
func SetLogger(adapter string, config ...string) error {
	for _, c := range config {
		var m map[string]interface{}
		if json.Unmarshal([]byte(c), &m) != nil {
			continue
		}
		if lvl, ok := m["level"].(string); ok {
			if l, err := luxlog.ParseLevel(lvl); err == nil {
				luxlog.SetGlobalLevel(l)
			}
		}
	}
	return nil
}

// Reset restores the default (info) log level.
func Reset() { luxlog.SetGlobalLevel(luxlog.InfoLevel) }

// format renders the leading value and args into a single message under the
// printf-plus-appended-fields contract the call sites rely on.
func format(f interface{}, v ...interface{}) string {
	var msg string
	switch t := f.(type) {
	case string:
		msg = t
		if len(v) == 0 {
			return msg
		}
		// A string with a verb is a fmt format; otherwise append the args
		// as " %v" fields.
		if !(strings.Contains(msg, "%") && !strings.Contains(msg, "%%")) {
			msg += strings.Repeat(" %v", len(v))
		}
	default:
		msg = fmt.Sprint(f)
		if len(v) == 0 {
			return msg
		}
		msg += strings.Repeat(" %v", len(v))
	}
	return fmt.Sprintf(msg, v...)
}
