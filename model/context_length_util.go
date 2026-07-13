// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2025 The OpenAgent Authors. All Rights Reserved.
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

package model

// DefaultContextLength is the floor used when a model declares no window.
//
// It is deliberately a FLOOR, not a guess. Every model Hanzo serves is at
// least 128K; a model that wants more declares it. Under-reporting truncates
// a prompt, over-reporting makes the upstream reject it — so when we do not
// know, we take the value that is safe on every model in the lineup.
const DefaultContextLength = 131072

// contextWindowResolver resolves a model's max context from configuration.
// controllers wires it to ModelConfig.ContextWindow at init, so models.yaml
// is the source of truth. Set once at startup, read on every request — no
// lock, init-time write only.
var contextWindowResolver func(model string) int

// SetContextWindowResolver installs the config-backed context-window lookup.
// Called once from controllers after ModelConfig is initialized.
func SetContextWindowResolver(f func(model string) int) { contextWindowResolver = f }

// GetContextLength returns the max context (tokens) for a model.
//
// Configuration is the ONLY source of truth. A model's window is whatever
// models.yaml declares (following the alias chain: zen5 → its upstream), and
// nothing else. There is no name-matching heuristic: the previous table
// dispatched on substrings of the model name ("deepseek", "v3", "32b", …) and
// every new model silently fell through it — deepseek-v4-pro matched no branch
// and got 16384, which 402'd every long prompt against a 1M-context model.
// A table that must be edited for each release is a table that is always one
// release out of date, so it is gone. Declare the window in models.yaml.
func GetContextLength(typ string) int {
	if contextWindowResolver != nil {
		if w := contextWindowResolver(typ); w > 0 {
			return w
		}
	}
	return DefaultContextLength
}
