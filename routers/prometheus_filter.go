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

package routers

import (
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
	"github.com/zap-proto/zip"
)

func recordSystemInfo(systemInfo *util.SystemInfo) {
	for i, value := range systemInfo.CpuUsage {
		object.CpuUsage.WithLabelValues(fmt.Sprintf("%d", i)).Set(value)
	}
	object.MemoryUsage.WithLabelValues("memoryUsed").Set(float64(systemInfo.MemoryUsed))
	object.MemoryUsage.WithLabelValues("memoryTotal").Set(float64(systemInfo.MemoryTotal))
}

func PrometheusFilter(c *zip.Ctx) error {
	method := c.Method()
	path := c.Path()
	if strings.HasPrefix(path, "/v1/metrics") {
		systemInfo, err := util.GetSystemInfo()
		if err == nil {
			recordSystemInfo(systemInfo)
		}
		return c.Continue()
	}

	// /v1, not /api. This service has never served an /api prefix — the brand rule
	// is /v1 only — so the counter it guards has been reporting zero throughput for
	// as long as it has existed.
	if strings.HasPrefix(path, "/v1") {
		c.Locals("startTime", time.Now())
		object.TotalThroughput.Inc()
		object.ApiThroughput.WithLabelValues(path, method).Inc()
	}
	return c.Continue()
}
