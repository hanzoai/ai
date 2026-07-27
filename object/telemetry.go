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
// opt-in via O11Y_ENDPOINT (or O11Y_TRACES_ENDPOINT),
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

	"github.com/hanzoai/ai/log"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	luxtrace "github.com/luxfi/trace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// genaiTracerName is the instrumentation scope for every GenAI span.
const genaiTracerName = "github.com/hanzoai/ai/genai"

var (
	telemetryProvider *sdktrace.TracerProvider
	telemetryReady    atomic.Bool
	// hostGenAITracer PINS the GenAI tracer to the tracer provider that owns the emit
	// path — the host's adopted provider (fused cloud) or ai's own (standalone) —
	// captured the moment that provider becomes the process-global. GenAITracer
	// returns it instead of re-resolving otel.Tracer on every emit, so a LATER
	// otel.SetTracerProvider by another in-process component cannot redirect gen_ai
	// spans off the o11y sink. nil until a provider is captured (pre-init / disabled).
	hostGenAITracer atomic.Pointer[trace.Tracer]
)

// AdoptHostTracerProvider declares that a host composition root has installed the
// process-global OTel tracer provider — the fused cloud binary does exactly this,
// wiring that provider to the embedded o11y in-process trace sink (Cost-0, no
// socket). Once adopted, the ai module PINS its GenAI tracer to that provider and
// emits every gen_ai span through it (one provider, one wire); InitTelemetry will
// NOT fork a competing exporter. It is the ONE signal a host uses to make ai ride
// its trace path: explicit and typed, not env archaeology. Idempotent, and MUST be
// called immediately after the host installs the provider — the composition root
// installs the provider, adopts it, then mounts ai.
//
// PINNING (capturing the tracer here rather than re-resolving otel.Tracer per emit)
// is load-bearing, not an optimization: another in-process component reassigns the
// process-global tracer provider when it starts during mount, AFTER this adopt. The
// confirmed culprit in the fused cloud binary is the embedded o11y (SigNoz) runtime
// self-instrumenting — hanzoai/o11y pkg/instrumentation/sdk.go does
// `otel.SetTracerProvider(sdk.TracerProvider())` — invoked via the o11y runtime
// start. OTel's global delegation upgrades tracers captured BEFORE the first
// SetTracerProvider (cloud's request-span tracer, captured at package init — so HTTP
// spans keep reaching the sink) but a tracer resolved AFTER the reassignment binds
// to the newcomer's provider. A per-emit otel.Tracer(genaiTracerName) would thus
// strand every gen_ai span on that provider and silently drop it — the exact symptom
// (HTTP spans land, gen_ai spans do not; worked before o11y was embedded, broke
// after) this pin fixes.
func AdoptHostTracerProvider() {
	captureGenAITracer()
	telemetryReady.Store(true)
}

// captureGenAITracer pins hostGenAITracer to the tracer the CURRENT process-global
// provider yields — the provider the caller installed immediately before. Called
// from AdoptHostTracerProvider (host mode) and initTelemetry (standalone), each
// right after its provider becomes global.
func captureGenAITracer() {
	t := otel.Tracer(genaiTracerName)
	hostGenAITracer.Store(&t)
}

// InitTelemetry wires the GenAI span emit path. There are two mutually exclusive
// modes, selected by who owns the process-global tracer provider:
//
//   - Host-owned (the fused cloud binary): the composition root installed the
//     provider and called AdoptHostTracerProvider first, so telemetryReady is
//     already latched. ai emits through the host's global provider and never forks
//     its own — that would win the global slot for the GenAI tracer created
//     afterward and split the wire, stranding gen_ai spans off the host's sink.
//   - Standalone (cmd/aid): ai owns the provider. Opt-in via
//     O11Y_ENDPOINT (or the traces-specific variant); it builds in the
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
		log.Info("telemetry: GenAI spans -> host global tracer provider")
		return
	}
	endpoint := firstNonEmptyEnv("O11Y_TRACES_ENDPOINT", "O11Y_ENDPOINT")
	if endpoint == "" {
		log.Info("telemetry: disabled (no host provider adopted; set O11Y_ENDPOINT to emit GenAI spans to o11y)")
		return
	}
	go initTelemetry()
}

func initTelemetry() {
	// ZAP carries a JSON SpanBatch to o11y/pkg/zapreceiver. luxtrace.New installs
	// its own provider, so this keeps the sdktrace one for the resource attributes
	// the o11y columns depend on and feeds it the ZAP exporter.
	exporter, err := luxtrace.NewZAPExporter(
		luxtrace.ExporterConfig{Type: luxtrace.ZAP, Endpoint: firstNonEmptyEnv("O11Y_TRACES_ENDPOINT", "O11Y_ENDPOINT")},
		telemetryServiceName(), "",
	)
	if err != nil {
		log.Error("telemetry: create ZAP exporter: %v", err)
		return
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(resource.NewSchemaless(
			attribute.String("service.name", telemetryServiceName()),
			// deployment.environment on the resource so o11y's Environment column
			// resolves instead of defaulting to "default". Env-overridable; a
			// telemetry-emitting binary is a real deployment, so "production" is the
			// honest default (local dev never emits — the exporter is unset).
			attribute.String("deployment.environment", deploymentEnvironment()),
		)),
	)
	otel.SetTracerProvider(tp)
	captureGenAITracer()
	telemetryProvider = tp
	telemetryReady.Store(true)
	log.Info("telemetry: GenAI spans -> o11y via OTLP (service.name=%s)", telemetryServiceName())
}

// TelemetryEnabled reports whether the OTLP trace exporter is live. The emit
// path gates on it to skip span construction entirely when telemetry is off.
func TelemetryEnabled() bool { return telemetryReady.Load() }

// GenAITracer returns the tracer for GenAI spans. Once a provider is adopted (host)
// or installed (standalone), it returns the PINNED tracer bound to that provider —
// never re-resolving the process-global, so a later otel.SetTracerProvider by
// another component (e.g. the embedded o11y runtime's self-instrumentation SDK)
// cannot redirect gen_ai spans. Before any provider is captured it falls back to the global (a
// no-op tracer until telemetry latches; emit callers gate on TelemetryEnabled).
func GenAITracer() trace.Tracer {
	if t := hostGenAITracer.Load(); t != nil {
		return *t
	}
	return otel.Tracer(genaiTracerName)
}

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
		log.Warn("telemetry: shutdown: %v", err)
	}
}

func telemetryServiceName() string {
	if v := strings.TrimSpace(os.Getenv("OTEL_SERVICE_NAME")); v != "" {
		return v
	}
	return "hanzo-ai"
}

// deploymentEnvironment resolves the process deployment environment for the OTel
// resource. Env-overridable (DEPLOYMENT_ENVIRONMENT / OTEL_DEPLOYMENT_ENVIRONMENT /
// ENVIRONMENT); defaults to "production" because telemetry only emits when an
// exporter/sink is configured — i.e. a real deployment, never local dev.
func deploymentEnvironment() string {
	if v := firstNonEmptyEnv("DEPLOYMENT_ENVIRONMENT", "OTEL_DEPLOYMENT_ENVIRONMENT", "ENVIRONMENT"); v != "" {
		return v
	}
	return "production"
}

func firstNonEmptyEnv(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}
