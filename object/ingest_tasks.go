// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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

// Durable ingest via hanzoai/tasks — THE one async system (per tasks/CONTRACT.md
// "there is no second async system"). A long ingest source (github repo, web crawl,
// s3 bucket) clones/fetches, chunks, and embeds thousands of blobs — far past the 5s
// sync budget — so it runs as a durable workflow: the HTTP caller gets a workflow id
// immediately and tracks progress in the ONE Tasks product, never a bespoke job log.
// A pure "upload" (inline text/documents) stays a fast sync call and never touches
// this path.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	tasksclient "github.com/hanzoai/tasks/pkg/sdk/client"
	"github.com/hanzoai/tasks/pkg/sdk/temporal"
	tasksworker "github.com/hanzoai/tasks/pkg/sdk/worker"
	"github.com/hanzoai/tasks/pkg/sdk/workflow"
)

// ingestTaskQueue is this workflow family's own queue (`<service>-<purpose>`); the
// worker polls it and nothing unrelated multiplexes onto it (CONTRACT §3).
const ingestTaskQueue = "ai-ingest"

// ErrTasksNotConfigured signals no dialer was injected (the composition root hasn't
// wired the tasks engine). The handler treats it as "fall back to inline ingest" so
// ingest still works before/without a tasks rollout — graceful, not a second async system.
var ErrTasksNotConfigured = tasksNotConfigured{}

type tasksNotConfigured struct{}

func (tasksNotConfigured) Error() string {
	return "ingest: tasks durable-execution engine not wired (no dialer injected)"
}

// IngestWorkflowInput is the durable workflow's typed input — the owner (bound to the
// authenticated principal, never client-trusted), the ingest request, and the accept
// language. JSON-serializable, no funcs/channels (CONTRACT §2).
type IngestWorkflowInput struct {
	Owner   string        `json:"owner"`
	Request IngestRequest `json:"request"`
	Lang    string        `json:"lang"`
}

// IngestWorkflow is the durable ingest job. It runs the whole clone→chunk→embed as one
// retried activity with a generous timeout; on a worker crash the engine re-runs it.
// The activity is idempotent (deterministic chunk IDs overwrite their own docs), so a
// retry re-indexes cleanly rather than duplicating.
func IngestWorkflow(ctx workflow.Context, in IngestWorkflowInput) (*IngestStats, error) {
	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		// A large repo clone + embed can run minutes; cap at 10m (CONTRACT: never above 10m).
		StartToCloseTimeout: 10 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    2 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    2 * time.Minute,
			MaximumAttempts:    3,
		},
	})
	var out IngestStats
	if err := workflow.ExecuteActivity(actCtx, ingestActivity, in).Get(actCtx, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ingestActivity is the side-effecting body: the SAME IngestSource the sync path runs.
// One implementation of ingest, called inline for uploads and from the workflow for
// long sources (DRY — the durability is orthogonal to what ingest does).
func ingestActivity(_ context.Context, in IngestWorkflowInput) (*IngestStats, error) {
	return IngestSource(in.Owner, &in.Request, in.Lang)
}

// dialer is HOW the composition root (cloud) reaches the tasks engine FOR AN ORG: it
// returns a client scoped to that org's namespace (CONTRACT §6 — namespace maps 1:1 to
// org, non-negotiable). ai is TRANSPORT-AGNOSTIC — it never dials or reads env; cloud
// embeds the engine and supplies an in-process/loopback ZAP dialer (mega fast, no HTTP,
// no cross-service auth). Set once at boot. Nil → EnqueueIngest reports
// ErrTasksNotConfigured and the handler runs ingest inline (always works).
var (
	dialer      func(org string) (tasksclient.Client, error)
	provMu      sync.Mutex
	orgWorkers  sync.Map             // org → tasksclient.Client (its ai-ingest worker already started)
	liveWorkers []tasksworker.Worker // RETAINS started workers for the process lifetime — else the
	// worker object is unreachable after ingestOrgClient returns and its poll loop can stop, so an
	// enqueued workflow is accepted by the engine but NEVER picked up (0 workers/queues; describe 404).
)

// SetIngestDialer injects the per-org dialer. Called once at boot by cloud.
func SetIngestDialer(d func(org string) (tasksclient.Client, error)) { dialer = d }

// ingestOrgClient returns the org's tasks client, lazily provisioning its per-org
// ai-ingest worker on first use (CONTRACT §6: "one client + worker per active org,
// lazily on first ExecuteWorkflow"). provMu serializes first-touch so an org never gets
// two workers; the fast path is a lock-free sync.Map hit.
func ingestOrgClient(org string) (tasksclient.Client, error) {
	if c, ok := orgWorkers.Load(org); ok {
		return c.(tasksclient.Client), nil
	}
	provMu.Lock()
	defer provMu.Unlock()
	if c, ok := orgWorkers.Load(org); ok {
		return c.(tasksclient.Client), nil
	}
	if dialer == nil {
		return nil, ErrTasksNotConfigured
	}
	cli, err := dialer(org)
	if err != nil {
		return nil, err
	}
	wk := tasksworker.New(cli, ingestTaskQueue, tasksworker.Options{})
	wk.RegisterWorkflow(IngestWorkflow)
	wk.RegisterActivity(ingestActivity)
	if err := wk.Start(); err != nil {
		return nil, fmt.Errorf("tasks worker start: %w", err)
	}
	liveWorkers = append(liveWorkers, wk) // retain (under provMu) so the poll loop is never GC'd
	// Let the worker's Subscribe round-trips land before the caller enqueues the first
	// workflow, so the very first ExecuteWorkflow dispatches to it instead of racing an
	// unregistered queue (mirrors the tasks engine's own e2e test).
	time.Sleep(150 * time.Millisecond)
	orgWorkers.Store(org, cli)
	return cli, nil
}

// ingestWorkflowID is the deterministic, owner-scoped workflow id — also the
// idempotency key: re-submitting the same source for the same store while it is still
// running returns the existing handle instead of a duplicate crawl/clone.
func ingestWorkflowID(owner string, req *IngestRequest) string {
	store := req.Store
	if store == "" {
		store = DefaultDocsStore
	}
	key := req.Source
	switch req.Source {
	case "github":
		if req.GitHub != nil {
			key += "-" + req.GitHub.Repo
		}
	case "crawl":
		if req.Crawl != nil {
			key += "-" + req.Crawl.URL
		}
	}
	return fmt.Sprintf("ingest-%s-%s-%s", owner, store, sanitizeWorkflowKey(key))
}

// sanitizeWorkflowKey keeps a workflow id readable + id-safe (no spaces/slashes).
func sanitizeWorkflowKey(s string) string {
	r := strings.NewReplacer("/", "_", " ", "_", ":", "_", "?", "_", "#", "_", "&", "_")
	return strings.Trim(r.Replace(s), "_")
}

// EnqueueIngest submits a long ingest as a durable workflow in the OWNER's namespace
// (CONTRACT §6) and returns its id immediately — the caller never blocks on the
// clone/chunk/embed. Returns ErrTasksNotConfigured when no dialer is wired so the
// handler falls back to inline.
func EnqueueIngest(ctx context.Context, owner string, req *IngestRequest, lang string) (string, error) {
	cli, err := ingestOrgClient(owner)
	if err != nil {
		return "", err
	}
	in := IngestWorkflowInput{Owner: owner, Request: *req, Lang: lang}
	run, err := cli.ExecuteWorkflow(ctx, tasksclient.StartWorkflowOptions{
		ID:        ingestWorkflowID(owner, req),
		TaskQueue: ingestTaskQueue,
	}, IngestWorkflow, in)
	if err != nil {
		return "", fmt.Errorf("tasks enqueue: %w", err)
	}
	return run.GetID(), nil
}

// IsAsyncIngestSource reports whether a source runs as a durable workflow (long) vs
// inline (fast). Upload/empty = inline; github/crawl/s3 = workflow.
func IsAsyncIngestSource(source string) bool {
	switch source {
	case "", "upload":
		return false
	default:
		return true
	}
}
