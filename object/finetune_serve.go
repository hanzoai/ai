// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2025 The OpenAgent Authors. All Rights Reserved.
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
package object

import (
	"fmt"
	"strings"

	"github.com/hanzoai/ai/util"
)

// RegisterServedModel writes the Provider + ModelRoute that make a served fine-tune
// callable via /v1/chat/completions and routed per-org. The predictor exposes an
// OpenAI-compatible API, so the provider is Type "Local" with compatibleProvider
// "openai". Idempotent upsert. cluster.DeployFinetune calls this once the
// InferenceService is up and passes its resolved status.url as base.
func RegisterServedModel(job *FinetuneJob, serviceName, modelId, base string) error {
	base = strings.TrimRight(base, "/")
	if base == "" {
		base = fmt.Sprintf("http://%s-predictor.%s.svc.cluster.local", serviceName, job.Namespace)
	}

	provider := &Provider{
		Owner:              job.Owner,
		Name:               serviceName,
		CreatedTime:        util.GetCurrentTime(),
		DisplayName:        firstNonEmpty(job.DisplayName, job.Name) + " (fine-tune)",
		Category:           "Model",
		Type:               "Local",
		SubType:            serviceName, // the served-model name the predictor answers to
		CompatibleProvider: "openai",
		ProviderUrl:        base + "/openai/v1",
		ClientSecret:       "", // in-cluster, unauthenticated predictor
		State:              "Active",
	}
	if err := upsertProvider(provider); err != nil {
		return err
	}

	return upsertModelRoute(&ModelRoute{
		Owner:     job.Owner,
		ModelName: modelId,
		Provider:  serviceName,
		Upstream:  serviceName,
		OwnedBy:   job.Owner,
		Enabled:   true,
	})
}

func upsertProvider(p *Provider) error {
	existing, _ := GetModelProviderByNameForOrg(p.Owner, p.Name)
	if existing != nil {
		return adapter.db.Model(p).Update()
	}
	_, err := AddProvider(p)
	return err
}

func upsertModelRoute(r *ModelRoute) error {
	existing, _ := GetModelRoute(r.Owner, r.ModelName)
	if existing != nil {
		_, err := UpdateModelRoute(r.Owner, r.ModelName, r)
		return err
	}
	_, err := AddModelRoute(r)
	return err
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
