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

// OTel GenAI telemetry — the OTLP trace exporter that ships one gen_ai span per
// LLM call to o11y (SigNoz), following the OpenTelemetry GenAI semantic
// conventions. This is the ONE way the ai module emits per-request LLM traces.
// It is orthogonal to the two usage writers: the spend ledger
// (hanzo.cloud_usage, written by zapWriteUsage) and the o11y-owned observations
// table are separate concerns — see controllers/openai_api.go recordTrace.
//
// Opt-in via OTEL_EXPORTER_OTLP_ENDPOINT (or OTEL_EXPORTER_OTLP_TRACES_ENDPOINT),
// mirroring InitDatastore's env-gated, background, non-fatal posture: with no
// endpoint the emitter stays honest-off (TelemetryEnabled() == false) and the
// span emit is a no-op, so local dev never ships to a nonexistent collector. The
// exporter itself self-configures from the standard OTEL_EXPORTER_OTLP_* env
// (endpoint, headers, per-scheme TLS, timeout) — never hard-coded.
package object

import (
	"context"
	"os"
	"strings"
	"sync/atomic"

	"github.com/beego/beego/logs"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// genaiTracerName is the instrumentation scope for every GenAI span.
const genaiTracerName = "github.com/hanzoai/ai/genai"

var (
	telemetryProvider *sdktrace.TracerProvider
	telemetryReady    atomic.Bool
)

// InitTelemetry opens the OTLP trace exporter that emits OTel GenAI spans to
// o11y. Opt-in via OTEL_EXPORTER_OTLP_ENDPOINT (or the traces-specific variant).
// Non-fatal and asynchronous: a collector outage at boot must never take down
// the cloud process, so it builds in the background and only latches ready once
// the provider is set. Until then TelemetryEnabled() reports false and the emit
// path is skipped. Runs identically in cmd/aid and the embedded cloud binary.
func InitTelemetry() {
	// When the host process already owns the trace path — cloud installs a
	// ZAP-native tracer provider and signals it via OTEL_EXPORTER_ZAP_ENDPOINT —
	// do NOT fork a competing OTLP provider (that would overwrite the host's
	// global provider and split the wire). Latch ready and emit GenAI spans
	// through the host's global tracer, so they ride the host's wire (ZAP).
	// One provider, one wire. The host owns flush/shutdown of that provider.
	if strings.TrimSpace(os.Getenv("OTEL_EXPORTER_ZAP_ENDPOINT")) != "" {
		telemetryReady.Store(true)
		logs.Info("telemetry: OTel GenAI spans -> host global tracer provider (ZAP wire)")
		return
	}
	endpoint := firstNonEmptyEnv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		logs.Info("telemetry: disabled (set OTEL_EXPORTER_OTLP_ENDPOINT to emit OTel GenAI spans to o11y)")
		return
	}
	go initTelemetry()
}

func initTelemetry() {
	// otlptracehttp self-configures from the standard OTEL_EXPORTER_OTLP_* env
	// vars (endpoint, headers, insecure-by-scheme, timeout).
	exporter, err := otlptracehttp.New(context.Background())
	if err != nil {
		logs.Error("telemetry: create OTLP exporter: %v", err)
		return
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(resource.NewSchemaless(
			attribute.String("service.name", telemetryServiceName()),
		)),
	)
	otel.SetTracerProvider(tp)
	telemetryProvider = tp
	telemetryReady.Store(true)
	logs.Info("telemetry: OTel GenAI spans -> o11y via OTLP (service.name=%s)", telemetryServiceName())
}

// TelemetryEnabled reports whether the OTLP trace exporter is live. The emit
// path gates on it to skip span construction entirely when telemetry is off.
func TelemetryEnabled() bool { return telemetryReady.Load() }

// GenAITracer returns the named tracer for GenAI spans. Safe before init: the
// global provider is a no-op until initTelemetry latches, and emit callers gate
// on TelemetryEnabled anyway.
func GenAITracer() trace.Tracer { return otel.Tracer(genaiTracerName) }

// ShutdownTelemetry flushes buffered spans and stops the exporter. Non-fatal and
// a no-op when telemetry was never enabled; the atomic gate establishes the
// happens-before with initTelemetry, so telemetryProvider is safely observed.
func ShutdownTelemetry(ctx context.Context) {
	if !telemetryReady.Load() {
		return
	}
	// In host-provider (ZAP) mode we never installed our own provider — the host
	// owns flush/shutdown of the global provider — so telemetryProvider is nil.
	if telemetryProvider == nil {
		return
	}
	if err := telemetryProvider.Shutdown(ctx); err != nil {
		logs.Warn("telemetry: shutdown: %v", err)
	}
}

func telemetryServiceName() string {
	if v := strings.TrimSpace(os.Getenv("OTEL_SERVICE_NAME")); v != "" {
		return v
	}
	return "hanzo-ai"
}

func firstNonEmptyEnv(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}
