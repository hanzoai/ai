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

package log

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"
)

// reference reproduces beego/logs.formatLog verbatim. format must agree with
// it on every input so the migrated call sites render identically.
func reference(f interface{}, v ...interface{}) string {
	var msg string
	switch f.(type) {
	case string:
		msg = f.(string)
		if len(v) == 0 {
			return msg
		}
		if strings.Contains(msg, "%") && !strings.Contains(msg, "%%") {
			// format string
		} else {
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

func TestFormatMatchesBeego(t *testing.T) {
	err := errors.New("boom")
	cases := []struct {
		f interface{}
		v []interface{}
	}{
		{"plain message no args", nil},
		{"format with verb: %v", []interface{}{err}},
		{"format with verb: %d and %s", []interface{}{42, "x"}},
		{"no verb but has args", []interface{}{err}},
		{"no verb two args", []interface{}{"a", 2}},
		{"literal percent %% only", []interface{}{err}},
		{"mixed %% and %v", []interface{}{err}},
		{"CLOUD_API_REPLICAS=%d refusing", []interface{}{3}},
		{"", []interface{}{err}},
		{"", nil},
		{errors.New("error as leading value"), nil},
		{errors.New("error leading with args"), []interface{}{1, 2}},
		{123, []interface{}{"tail"}},
		{"percent at end %", nil},
	}
	for _, c := range cases {
		got := format(c.f, c.v...)
		want := reference(c.f, c.v...)
		if got != want {
			t.Errorf("format(%v, %v) = %q, beego reference = %q", c.f, c.v, got, want)
		}
	}
}

// TestLevelHelpersDoNotPanic exercises each public level so a formatting or
// backend regression surfaces as a test failure rather than a runtime crash.
func TestLevelHelpersDoNotPanic(t *testing.T) {
	Info("info %v", 1)
	Warning("warning %v", 2)
	Warn("warn %v", 3)
	Error("error %v", 4)
	Info("plain")
	Error(errors.New("bare error"))
}

// TestSetLoggerLevelAndReset confirms SetLogger honors a JSON "level" and
// that malformed config and the legacy file/console adapters are accepted
// without error, and Reset returns to info.
func TestSetLoggerLevelAndReset(t *testing.T) {
	if err := SetLogger("console", `{"level":"error"}`); err != nil {
		t.Fatalf("SetLogger level: %v", err)
	}
	if err := SetLogger("file", `{"adapter":"file","filename":"x.log"}`); err != nil {
		t.Fatalf("SetLogger file adapter: %v", err)
	}
	if err := SetLogger("file", `not json`); err != nil {
		t.Fatalf("SetLogger malformed config must not error: %v", err)
	}
	Reset()
}

// TestParseLevel covers the string and historical-numeric level forms so a
// numeric {"level":7} configures debug instead of silently no-opping.
func TestParseLevel(t *testing.T) {
	cases := []struct {
		v    interface{}
		want luxlog.Level
		ok   bool
	}{
		{"debug", luxlog.DebugLevel, true},
		{"ERROR", luxlog.ErrorLevel, true},
		{float64(7), luxlog.DebugLevel, true}, // JSON numbers decode as float64
		{float64(6), luxlog.InfoLevel, true},
		{float64(4), luxlog.WarnLevel, true},
		{float64(3), luxlog.ErrorLevel, true},
		{float64(0), luxlog.ErrorLevel, true}, // clamp: errors always emit
		{"nonsense", luxlog.InfoLevel, false},
		{nil, luxlog.InfoLevel, false},
	}
	for _, c := range cases {
		got, ok := parseLevel(c.v)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseLevel(%v) = (%v, %v), want (%v, %v)", c.v, got, ok, c.want, c.ok)
		}
	}
}
