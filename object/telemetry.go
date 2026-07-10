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
// LLM call to o11y, following the OpenTelemetry GenAI semantic
// conventions. This is the ONE way the ai module emits per-request LLM traces.
// It is orthogonal to the two usage writers: the spend ledger
// (hanzo.cloud_usage, written by zapWriteUsage) and the o11y-owned observations
// table are separate concerns — see controllers/openai_api.go recordTrace.
//
// In the fused cloud binary the composition root installs the process-global
// tracer provider (wired to the embedded o11y in-process trace sink) and calls
// AdoptHostTracerProvider; ai then emits every gen_ai span through that provider —
// one provider, one wire — and does NOT install its own. Standalone (cmd/aid) is
// opt-in via OTEL_EXPORTER_OTLP_ENDPOINT (or OTEL_EXPORTER_OTLP_TRACES_ENDPOINT),
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

// AdoptHostTracerProvider declares that a host composition root has installed the
// process-global OTel tracer provider — the fused cloud binary does exactly this,
// wiring that provider to the embedded o11y in-process trace sink (Cost-0, no
// socket). Once adopted, the ai module emits every gen_ai span through that global
// provider (one provider, one wire) and InitTelemetry will NOT fork a competing
// exporter. It is the ONE signal a host uses to make ai ride its trace path:
// explicit and typed, not env archaeology. Idempotent, and safe to call before
// InitTelemetry — the composition root installs the provider, adopts it, then
// mounts ai. The host owns the provider's flush/shutdown.
func AdoptHostTracerProvider() { telemetryReady.Store(true) }

// InitTelemetry wires the GenAI span emit path. There are two mutually exclusive
// modes, selected by who owns the process-global tracer provider:
//
//   - Host-owned (the fused cloud binary): the composition root installed the
//     provider and called AdoptHostTracerProvider first, so telemetryReady is
//     already latched. ai emits through the host's global provider and never forks
//     its own — that would win the global slot for the GenAI tracer created
//     afterward and split the wire, stranding gen_ai spans off the host's sink.
//   - Standalone (cmd/aid): ai owns the provider. Opt-in via
//     OTEL_EXPORTER_OTLP_ENDPOINT (or the traces-specific variant); it builds in the
//     background and only latches ready once the provider is set. With no endpoint
//     the emitter stays honest-off (TelemetryEnabled() == false) and the emit is a
//     no-op, so local dev never ships to a nonexistent collector.
//
// Non-fatal and asynchronous: a collector outage at boot never takes the process
// down. Runs identically in cmd/aid and the embedded cloud binary.
func InitTelemetry() {
	// Host already owns the global provider (AdoptHostTracerProvider was called by
	// the composition root before this mount): emit through it, never fork.
	if telemetryReady.Load() {
		logs.Info("telemetry: OTel GenAI spans -> host global tracer provider")
		return
	}
	endpoint := firstNonEmptyEnv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		logs.Info("telemetry: disabled (no host provider adopted; set OTEL_EXPORTER_OTLP_ENDPOINT to emit OTel GenAI spans to o11y)")
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
