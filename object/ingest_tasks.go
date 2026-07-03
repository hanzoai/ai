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
	"os"
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

// ErrTasksNotConfigured signals no durable-execution engine is wired (TASKS_ADDR
// unset). The handler treats it as "fall back to inline ingest" so ingest still works
// before/without a tasks rollout — a graceful degradation, not a second async system.
var ErrTasksNotConfigured = tasksNotConfigured{}

type tasksNotConfigured struct{}

func (tasksNotConfigured) Error() string {
	return "ingest: tasks durable-execution backend not configured (TASKS_ADDR unset)"
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

// tasksAddr is the durable-execution engine address; empty → in-process/unconfigured.
func tasksAddr() string { return strings.TrimSpace(os.Getenv("TASKS_ADDR")) }

var (
	ingestOnce sync.Once
	ingestCli  tasksclient.Client
	ingestErr  error
)

// ingestClient lazily dials the tasks engine and starts the ingest worker exactly once
// (the ingest feature owns its own worker lifecycle — no boot wiring needed wherever
// this library is embedded). Returns (nil, nil) when TASKS_ADDR is unset so the caller
// falls back to inline ingest. The worker + client share one connection; the worker
// polls ai-ingest for the lifetime of the process.
func ingestClient() (tasksclient.Client, error) {
	ingestOnce.Do(func() {
		addr := tasksAddr()
		if addr == "" {
			return // unconfigured; ingestCli stays nil → inline fallback
		}
		cli, err := tasksclient.Dial(tasksclient.Options{HostPort: addr})
		if err != nil {
			ingestErr = fmt.Errorf("tasks dial: %w", err)
			return
		}
		if err := startIngestWorker(cli); err != nil {
			ingestErr = err
			return
		}
		ingestCli = cli
	})
	return ingestCli, ingestErr
}

// startIngestWorker registers the ingest workflow + activity on the ai-ingest queue and
// starts polling. Extracted so a test can drive it against an embedded engine.
func startIngestWorker(cli tasksclient.Client) error {
	wk := tasksworker.New(cli, ingestTaskQueue, tasksworker.Options{})
	wk.RegisterWorkflow(IngestWorkflow)
	wk.RegisterActivity(ingestActivity)
	if err := wk.Start(); err != nil {
		return fmt.Errorf("tasks worker start: %w", err)
	}
	return nil
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

// EnqueueIngest submits a long ingest as a durable workflow and returns its id
// immediately — the caller never blocks on the clone/chunk/embed. Returns
// ErrTasksNotConfigured when no engine is wired so the handler can fall back to inline.
func EnqueueIngest(ctx context.Context, owner string, req *IngestRequest, lang string) (string, error) {
	cli, err := ingestClient()
	if err != nil {
		return "", err
	}
	if cli == nil {
		return "", ErrTasksNotConfigured
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
