// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package audio

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
)

// Finding is one live-lane verdict: what was probed, whether it held, and the
// detail a human can reproduce from — the QA report the issue's acceptance
// criteria demand. Offline test failures never land here; they fail as ordinary
// go test failures, which is what makes CI usable.
type Finding struct {
	Lane   string `json:"lane"`
	Check  string `json:"check"`
	Status string `json:"status"` // pass | fail
	Detail string `json:"detail,omitempty"`
}

// Report collects findings across live lanes and writes them to HANZO_QA_REPORT
// as a JSON artifact plus a Markdown twin beside it.
type Report struct {
	mu       sync.Mutex
	findings []Finding
}

func (r *Report) Add(lane, check string, ok bool, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	status := "pass"
	if !ok {
		status = "fail"
	}
	r.findings = append(r.findings, Finding{Lane: lane, Check: check, Status: status, Detail: clip(detail)})
}

// MaybeWrite writes both artifacts when HANZO_QA_REPORT names a path; a no-op
// otherwise, so offline runs leave the tree clean.
func (r *Report) MaybeWrite() error {
	path := os.Getenv(ReportEnv)
	if path == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	findings := append([]Finding(nil), r.findings...)
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Lane != findings[j].Lane {
			return findings[i].Lane < findings[j].Lane
		}
		return findings[i].Check < findings[j].Check
	})
	raw, err := json.MarshalIndent(map[string]any{"suite": "qa/audio #133", "findings": findings}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	var md strings.Builder
	md.WriteString("# qa/audio #133 — live findings\n\n| lane | check | status | detail |\n|---|---|---|---|\n")
	for _, f := range findings {
		md.WriteString(fmt.Sprintf("| %s | %s | %s | %s |\n", f.Lane, f.Check, f.Status, strings.ReplaceAll(f.Detail, "|", `|`)))
	}
	if err := os.WriteFile(strings.TrimSuffix(path, ".json")+".md", []byte(md.String()), 0o644); err != nil {
		return err
	}
	return nil
}

// live is the shared collector the live lanes report through; TestMain flushes
// it when the run ends.
var live = &Report{}
