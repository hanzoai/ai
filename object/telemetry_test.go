// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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

package object

import (
	"context"
	"testing"
)

// TestTelemetryDisabledByDefault asserts the honest-off posture: with no
// InitTelemetry call (no OTEL_EXPORTER_OTLP_ENDPOINT), telemetry is disabled,
// GenAITracer is still safe to call (noop tracer), and ShutdownTelemetry is a
// safe no-op — so the emit path never breaks a request in local/dev.
func TestTelemetryDisabledByDefault(t *testing.T) {
	if TelemetryEnabled() {
		t.Fatal("TelemetryEnabled() must be false when InitTelemetry was never called")
	}
	if GenAITracer() == nil {
		t.Fatal("GenAITracer() must never be nil (noop tracer when telemetry is off)")
	}
	ShutdownTelemetry(context.Background()) // must not panic when disabled
}

func TestTelemetryServiceNameDefault(t *testing.T) {
	t.Setenv("OTEL_SERVICE_NAME", "")
	if got := telemetryServiceName(); got != "hanzo-ai" {
		t.Errorf("telemetryServiceName() = %q, want \"hanzo-ai\"", got)
	}
	t.Setenv("OTEL_SERVICE_NAME", "custom-svc")
	if got := telemetryServiceName(); got != "custom-svc" {
		t.Errorf("telemetryServiceName() = %q, want \"custom-svc\"", got)
	}
}
