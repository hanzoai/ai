// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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
package routers

import (
	"github.com/hanzoai/beego"

	"github.com/hanzoai/ai/controllers"
)

// init registers the /v1/finetune/* fine-tuning broker routes. They live in their
// OWN file (not router.go) on purpose: Go runs every init() in the `routers`
// package before Beego serves, so these registrations compose with the main route
// table additively — the finetune surface is added without editing the shared
// router.go. Verb:method pairs are semicolon-separated, exactly like the wecom-bot
// route in router.go.
func init() {
	// Job lifecycle.
	beego.Router("/v1/finetune/jobs", &controllers.ApiController{}, "GET:ListFinetuneJobs;POST:CreateFinetuneJob")
	beego.Router("/v1/finetune/job", &controllers.ApiController{}, "GET:GetFinetuneJob")
	beego.Router("/v1/finetune/cancel", &controllers.ApiController{}, "POST:CancelFinetuneJob")
	beego.Router("/v1/finetune/deploy", &controllers.ApiController{}, "POST:DeployFinetuneJob")

	// Recommended defaults (the "Unsloth-class" presets brain).
	beego.Router("/v1/finetune/presets", &controllers.ApiController{}, "GET:GetFinetunePresets")

	// HuggingFace discovery (base-model + dataset pickers, private via KMS token).
	beego.Router("/v1/finetune/hf/models", &controllers.ApiController{}, "GET:SearchHfModels")
	beego.Router("/v1/finetune/hf/datasets", &controllers.ApiController{}, "GET:SearchHfDatasets")
	beego.Router("/v1/finetune/hf/repo", &controllers.ApiController{}, "GET:GetHfRepo")
}
