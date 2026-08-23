// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2023 The OpenAgent Authors. All Rights Reserved.
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

package i18n

import (
	"embed"
	"fmt"
	"strings"
	"sync"

	"github.com/hanzoai/ai/util"
)

//go:embed locales/*/data.json
var f embed.FS

var langMap = make(map[string]map[string]map[string]string) // for example : langMap[en][account][Invalid information] = Invalid information

// langMu guards langMap, which is filled on FIRST USE of each language.
//
// It had no guard. Translate is called from the filter chain on every error that
// carries a translation key, so two requests arriving in an unloaded language
// wrote the same map at the same time — and a concurrent map write is a Go
// runtime FATAL ERROR, not a panic: recover() does not see it and the process
// ends. The one place a lazily-filled global is read on every request is the last
// place to leave unsynchronised.
var langMu sync.RWMutex

type I18nData map[string]map[string]string

func Translate(language string, errorText string) string {
	tokens := strings.SplitN(errorText, ":", 2)
	if !strings.Contains(errorText, ":") || len(tokens) != 2 {
		return fmt.Sprintf("Translate error: the error text doesn't contain \":\", errorText = %s", errorText)
	}

	messages := locale(language)
	if messages == nil {
		if language == "en" {
			return tokens[1]
		}
		return Translate("en", errorText)
	}

	res := messages[tokens[0]][tokens[1]]
	if res == "" {
		res = tokens[1]
	}
	return res
}

// locale returns one language's strings, reading and parsing it on first use.
//
// A language the build does not carry, or a file it cannot parse, answers nil so
// the caller can fall back — parsing embedded data used to panic here, which
// turns a build-time mistake into a request-time one.
func locale(language string) I18nData {
	langMu.RLock()
	loaded := langMap[language]
	langMu.RUnlock()
	if loaded != nil {
		return loaded
	}

	file, err := f.ReadFile(fmt.Sprintf("locales/%s/data.json", language))
	if err != nil {
		return nil
	}
	parsed := I18nData{}
	if err := util.JsonToStruct(string(file), &parsed); err != nil {
		return nil
	}

	langMu.Lock()
	defer langMu.Unlock()
	// Another goroutine may have parsed it while this one was reading; keep the
	// copy already in the map so every caller sees the same one.
	if existing := langMap[language]; existing != nil {
		return existing
	}
	langMap[language] = parsed
	return parsed
}
