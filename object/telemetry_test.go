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

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
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

// TestAdoptHostTracerProvider is the fused-binary contract and the fix for the
// gen_ai-spans-never-emitted root cause. In in-process-sink mode the cloud host
// sets NO OTLP/ZAP exporter endpoint, so pre-fix InitTelemetry took the "disabled"
// path and TelemetryEnabled() stayed false — emitGenAISpan no-oped on every LLM
// call even though the host's global provider (feeding o11y_traces) was live.
// After the host calls AdoptHostTracerProvider, ai must (1) latch ready so the emit
// path runs, and (2) NOT fork its own provider in InitTelemetry (telemetryProvider
// stays nil — the host owns flush/shutdown), so spans ride the host's ONE provider.
func TestAdoptHostTracerProvider(t *testing.T) {
	prevReady := telemetryReady.Load()
	prevProvider := telemetryProvider
	prevPin := hostGenAITracer.Load()
	t.Cleanup(func() {
		telemetryReady.Store(prevReady)
		telemetryProvider = prevProvider
		hostGenAITracer.Store(prevPin)
	})

	// Reproduce the in-process-sink host: no ai-owned exporter endpoint set.
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	telemetryReady.Store(false)
	telemetryProvider = nil

	AdoptHostTracerProvider()
	if !TelemetryEnabled() {
		t.Fatal("AdoptHostTracerProvider() must latch TelemetryEnabled() true so emitGenAISpan runs")
	}

	InitTelemetry() // host owns the provider: must not fork a competing one
	if telemetryProvider != nil {
		t.Fatal("InitTelemetry() forked an ai-owned provider despite host adoption; gen_ai spans must ride the host global provider")
	}
	if !TelemetryEnabled() {
		t.Fatal("TelemetryEnabled() must stay true after InitTelemetry in host-adopted mode")
	}
}

// TestGenAITracer_PinnedThroughGlobalClobber is the regression test for the LIVE
// prod failure: gen_ai spans stopped landing in o11y_traces even though the emit
// path ran and the host provider was adopted (telemetryReady true). Cause: the fused
// cloud binary embeds the o11y (SigNoz) runtime, whose self-instrumentation SDK
// (hanzoai/o11y pkg/instrumentation/sdk.go) calls otel.SetTracerProvider when it
// starts (during mount, AFTER adopt), reassigning the process-global provider. A
// per-emit otel.Tracer(...) then bound gen_ai spans to that provider and dropped
// them — while cloud's request-span tracer, captured at package init, survived (so
// HTTP spans kept landing, gen_ai did not). The fix PINS the GenAI tracer at adopt
// time; this asserts the pin holds THROUGH a later global SetTracerProvider.
func TestGenAITracer_PinnedThroughGlobalClobber(t *testing.T) {
	prevReady := telemetryReady.Load()
	prevPin := hostGenAITracer.Load()
	t.Cleanup(func() { telemetryReady.Store(prevReady); hostGenAITracer.Store(prevPin) })

	// Host installs its provider (the o11y in-process sink) and adopts it.
	host := sdktrace.NewTracerProvider()
	otel.SetTracerProvider(host)
	AdoptHostTracerProvider()
	wantHost := host.Tracer(genaiTracerName)

	// A later in-process component (the embedded OTel Collector) installs its OWN
	// provider as the process-global — the exact clobber that dropped gen_ai spans.
	otel.SetTracerProvider(sdktrace.NewTracerProvider())

	// Premise guard: the global WAS reassigned (a fresh resolve no longer yields the
	// host tracer) — otherwise this test would pass vacuously.
	if fresh := otel.Tracer(genaiTracerName); fresh == wantHost {
		t.Fatal("test premise moot: the second SetTracerProvider did not replace the global tracer")
	}
	// The fix: GenAITracer stays PINNED to the host provider, so gen_ai spans still
	// reach the o11y sink — immune to the collector's global reassignment.
	if got := GenAITracer(); got != wantHost {
		t.Fatal("GenAITracer must stay pinned to the adopted host provider after a later global SetTracerProvider (the embedded-collector clobber); else gen_ai spans strand on the collector's provider and never reach o11y_traces")
	}
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
