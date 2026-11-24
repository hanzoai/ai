// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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

package controllers

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/account"
	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
	"github.com/zap-proto/zip"
)

func setupThreadTestApp(t *testing.T) (*zip.App, *object.ThreadStore) {
	t.Helper()
	store := object.NewMemoryThreadStore()
	prevStore := object.DefaultThreadStore()
	object.SetDefaultThreadStore(store)
	prevHub := DefaultResponseStreamHub()
	SetDefaultResponseStreamHub(NewResponseStreamHub())
	t.Cleanup(func() {
		object.SetDefaultThreadStore(prevStore)
		SetDefaultResponseStreamHub(prevHub)
	})

	app := zip.New(zip.Config{DisableStartupMessage: true, ReadBufferSize: 32 << 10})
	bind := func(method, path string, fn func(*ApiController)) {
		app.Raw(method, path, func(c *zip.Ctx) error {
			fn(&ApiController{Ctx: c})
			return nil
		})
	}
	bind(http.MethodPost, "/v1/threads", (*ApiController).CreateThread)
	bind(http.MethodGet, "/v1/threads/:id", (*ApiController).GetThread)
	bind(http.MethodPost, "/v1/threads/:id/append", (*ApiController).AppendThread)
	bind(http.MethodPost, "/v1/threads/:id/fork", (*ApiController).ForkThread)
	bind(http.MethodPost, "/v1/threads/:id/merge", (*ApiController).MergeThread)
	bind(http.MethodPost, "/v1/threads/:id/handoff", (*ApiController).HandoffThread)
	bind(http.MethodPost, "/v1/threads/:id/grants", (*ApiController).AddThreadGrant)
	bind(http.MethodDelete, "/v1/threads/:id/grants/:principal", (*ApiController).DeleteThreadGrant)
	bind(http.MethodGet, "/v1/threads/:id/chunks/:chunkId", (*ApiController).GetThreadChunk)
	bind(http.MethodPost, "/v1/threads/:id/search", (*ApiController).SearchThreadChunks)
	bind(http.MethodGet, "/v1/responses/:id/stream", (*ApiController).StreamResponse)
	return app, store
}

func doThreadReq(t *testing.T, app *zip.App, method, target, token string, body any) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		rdr = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, target, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		if !strings.HasPrefix(token, "Bearer ") {
			token = "Bearer " + token
		}
		req.Header.Set("Authorization", token)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, out
}

func TestThreadAPI_ForkMergeHandoffConflictAndCrossAccountACLs(t *testing.T) {
	withStore(t)
	people := withIAM(t)
	app, _ := setupThreadTestApp(t)

	aliceTok := people.signedInOrgs(t, &iam.User{Owner: "acme", Name: "alice"}, account.OrgRef{Org: "acme", Role: "owner"})
	bobTok := people.signedInOrgs(t, &iam.User{Owner: "partner", Name: "bob"}, account.OrgRef{Org: "partner", Role: "owner"})
	eveTok := people.signedInOrgs(t, &iam.User{Owner: "stranger", Name: "eve"}, account.OrgRef{Org: "stranger", Role: "owner"})

	// 1. Create thread as acme/alice.
	status, raw := doThreadReq(t, app, http.MethodPost, "/v1/threads", aliceTok, map[string]any{
		"title": "multi-agent 10m migration",
	})
	if status != http.StatusOK {
		t.Fatalf("POST /v1/threads = %d: %s", status, raw)
	}
	var thr object.Thread
	if err := json.Unmarshal(raw, &thr); err != nil {
		t.Fatalf("unmarshal thread: %v", err)
	}
	if !strings.HasPrefix(thr.ID, "thr_") || thr.Org != "acme" {
		t.Fatalf("created thread = %+v", thr)
	}

	// 2. Append initial chunks at epoch 0.
	status, raw = doThreadReq(t, app, http.MethodPost, "/v1/threads/"+thr.ID+"/append", aliceTok, map[string]any{
		"branch":      "main",
		"lease_epoch": 0,
		"role":        "system",
		"pinned":      true,
		"content":     "Preserve cache-stable prefix across all turns.",
	})
	if status != http.StatusOK {
		t.Fatalf("append 1 = %d: %s", status, raw)
	}
	status, raw = doThreadReq(t, app, http.MethodPost, "/v1/threads/"+thr.ID+"/append", aliceTok, map[string]any{
		"branch":      "main",
		"lease_epoch": 0,
		"role":        "user",
		"content":     "Investigate quorum_snapshot_checksum_mismatch in Raft storage engine.",
	})
	if status != http.StatusOK {
		t.Fatalf("append 2 = %d: %s", status, raw)
	}
	var appendResp struct {
		Chunk object.Chunk `json:"chunk"`
	}
	_ = json.Unmarshal(raw, &appendResp)
	chunk2ID := appendResp.Chunk.ID

	// 3. Unauthorized cross-account caller MUST receive 403.
	if st, _ := doThreadReq(t, app, http.MethodGet, "/v1/threads/"+thr.ID, bobTok, nil); st != http.StatusForbidden {
		t.Fatalf("ungranted bob GET = %d, want 403", st)
	}
	if st, _ := doThreadReq(t, app, http.MethodGet, "/v1/threads/"+thr.ID, eveTok, nil); st != http.StatusForbidden {
		t.Fatalf("stranger eve GET = %d, want 403", st)
	}

	// 4. Grant read+append+handoff to user:partner/bob.
	status, raw = doThreadReq(t, app, http.MethodPost, "/v1/threads/"+thr.ID+"/grants", aliceTok, map[string]any{
		"principal":   "user:partner/bob",
		"scopes":      []string{"read", "append", "handoff"},
		"ttl_seconds": 3600,
	})
	if status != http.StatusOK {
		t.Fatalf("POST grants = %d: %s", status, raw)
	}

	// Cross-account bob can now read, search, recall chunks, and handoff, but NOT fork!
	if st, _ := doThreadReq(t, app, http.MethodGet, "/v1/threads/"+thr.ID, bobTok, nil); st != http.StatusOK {
		t.Fatalf("granted bob GET = %d, want 200", st)
	}
	if st, _ := doThreadReq(t, app, http.MethodPost, "/v1/threads/"+thr.ID+"/fork", bobTok, map[string]any{
		"branch": "unauthorized-fork",
	}); st != http.StatusForbidden {
		t.Fatalf("bob fork without fork scope = %d, want 403", st)
	}

	// Lossless search & byte-range recall by granted collaborator.
	status, raw = doThreadReq(t, app, http.MethodPost, "/v1/threads/"+thr.ID+"/search", bobTok, map[string]any{
		"branch": "main",
		"query":  "quorum_snapshot_checksum_mismatch",
		"limit":  5,
	})
	if status != http.StatusOK || !strings.Contains(string(raw), chunk2ID) {
		t.Fatalf("POST search = %d: %s", status, raw)
	}
	status, raw = doThreadReq(t, app, http.MethodGet, "/v1/threads/"+thr.ID+"/chunks/"+chunk2ID+"?byte_offset=12&byte_limit=33", bobTok, nil)
	if status != http.StatusOK || !strings.Contains(string(raw), "quorum_snapshot_checksum_mismatch") {
		t.Fatalf("GET chunk slice = %d: %s", status, raw)
	}

	// 5. Atomic handoff epoch 0 -> 1 to partner/bob on machine gpu-worker-9.
	status, raw = doThreadReq(t, app, http.MethodPost, "/v1/threads/"+thr.ID+"/handoff", aliceTok, map[string]any{
		"branch":        "main",
		"from_epoch":    0,
		"to_actor":      "bob",
		"to_org":        "partner",
		"to_machine":    "gpu-worker-9",
		"workspace_ref": "ws://hanzo/ai",
		"git_ref":       "refs/heads/feat/10m",
		"model_hint":    "enso-ultra",
		"note":          "Run full verification suite on gpu-worker-9",
	})
	if status != http.StatusOK {
		t.Fatalf("POST handoff = %d: %s", status, raw)
	}
	var handoffResp struct {
		Lease object.ThreadLease `json:"lease"`
		Chunk object.Chunk       `json:"chunk"`
	}
	_ = json.Unmarshal(raw, &handoffResp)
	if handoffResp.Lease.Epoch != 1 || handoffResp.Lease.MachineID != "gpu-worker-9" || handoffResp.Chunk.Kind != "handoff" {
		t.Fatalf("handoff response = %+v", handoffResp)
	}

	// 6. Stale-epoch append (epoch 0) MUST return HTTP 409 Conflict!
	if st, body := doThreadReq(t, app, http.MethodPost, "/v1/threads/"+thr.ID+"/append", aliceTok, map[string]any{
		"branch":      "main",
		"lease_epoch": 0,
		"role":        "assistant",
		"content":     "stale agent write",
	}); st != http.StatusConflict {
		t.Fatalf("stale epoch append = %d (%s), want 409", st, body)
	}

	// 7. New lease holder partner/bob appends at epoch 1 -> 200 OK!
	if st, body := doThreadReq(t, app, http.MethodPost, "/v1/threads/"+thr.ID+"/append", bobTok, map[string]any{
		"branch":      "main",
		"lease_epoch": 1,
		"role":        "assistant",
		"content":     "All checks passed on gpu-worker-9.",
	}); st != http.StatusOK {
		t.Fatalf("epoch 1 append = %d (%s), want 200", st, body)
	}

	// 8. Owner forks O(1) branch and merges it back into main.
	if st, body := doThreadReq(t, app, http.MethodPost, "/v1/threads/"+thr.ID+"/fork", aliceTok, map[string]any{
		"source_branch": "main",
		"from_chunk_id": chunk2ID,
		"branch":        "alt-fix",
	}); st != http.StatusOK {
		t.Fatalf("POST fork = %d (%s), want 200", st, body)
	}
	if st, body := doThreadReq(t, app, http.MethodPost, "/v1/threads/"+thr.ID+"/append", aliceTok, map[string]any{
		"branch":  "alt-fix",
		"role":    "assistant",
		"content": "Alternative fix verified.",
	}); st != http.StatusOK {
		t.Fatalf("append alt-fix = %d (%s), want 200", st, body)
	}
	if st, body := doThreadReq(t, app, http.MethodPost, "/v1/threads/"+thr.ID+"/merge", aliceTok, map[string]any{
		"source_branch": "alt-fix",
		"target_branch": "main",
		"lease_epoch":   1,
	}); st != http.StatusOK {
		t.Fatalf("POST merge = %d (%s), want 200", st, body)
	}

	// 9. Revoke bob's grant -> bob is immediately locked out (403).
	escapedPrincipal := url.PathEscape("user:partner/bob")
	if st, body := doThreadReq(t, app, http.MethodDelete, "/v1/threads/"+thr.ID+"/grants/"+escapedPrincipal, aliceTok, nil); st != http.StatusOK {
		t.Fatalf("DELETE grant = %d (%s), want 200", st, body)
	}
	if st, _ := doThreadReq(t, app, http.MethodGet, "/v1/threads/"+thr.ID, bobTok, nil); st != http.StatusForbidden {
		t.Fatalf("bob GET after revoke = %d, want 403", st)
	}
}

func TestThreadAPI_ResumableStreamReplayAfterSeqAndLiveTail(t *testing.T) {
	withStore(t)
	people := withIAM(t)
	app, store := setupThreadTestApp(t)

	aliceTok := people.signedInOrgs(t, &iam.User{Owner: "acme", Name: "alice"}, account.OrgRef{Org: "acme", Role: "owner"})
	eveTok := people.signedInOrgs(t, &iam.User{Owner: "stranger", Name: "eve"}, account.OrgRef{Org: "stranger", Role: "owner"})
	bobTok := people.signedInOrgs(t, &iam.User{Owner: "partner", Name: "bob"}, account.OrgRef{Org: "partner", Role: "owner"})

	thr, err := store.CreateThread("acme", "alice", "stream thread", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = store.AddGrant(thr.ID, object.Grant{Principal: "user:partner/bob", Scopes: []string{object.ThreadScopeRead}})

	rs := DefaultResponseStreamHub().Register("resp_live_1", "acme", "alice", thr.ID)
	rs.Append("response.created", []byte(`{"type":"response.created","sequence_number":0}`))
	rs.Append("response.output_text.delta", []byte(`{"delta":"chunk-1"}`))
	rs.Append("response.output_text.delta", []byte(`{"delta":"chunk-2"}`))

	// Unauthorized stranger gets 403.
	if st, _ := doThreadReq(t, app, http.MethodGet, "/v1/responses/resp_live_1/stream?after_seq=1", eveTok, nil); st != http.StatusForbidden {
		t.Fatalf("stranger stream replay = %d, want 403", st)
	}

	// Emit a live frame + complete asynchronously while subscriber tails!
	go func() {
		time.Sleep(25 * time.Millisecond)
		rs.Append("response.output_text.delta", []byte(`{"delta":"chunk-3-live"}`))
		rs.Append("response.completed", []byte(`{"type":"response.completed"}`))
		rs.Complete()
	}()

	// Resume from after_seq=1 as granted partner/bob: MUST skip seq 1, replay seq 2-3,
	// live-tail seq 4-5, and terminate with data: [DONE]!
	st, raw := doThreadReq(t, app, http.MethodGet, "/v1/responses/resp_live_1/stream?after_seq=1", bobTok, nil)
	if st != http.StatusOK {
		t.Fatalf("bob stream replay = %d: %s", st, raw)
	}
	wire := string(raw)
	if strings.Contains(wire, "id: 1\n") {
		t.Fatalf("after_seq=1 must NOT replay frame 1:\n%s", wire)
	}
	assertInOrder(t, wire,
		"id: 2\nevent: response.output_text.delta\ndata: {\"delta\":\"chunk-1\"}",
		"id: 3\nevent: response.output_text.delta\ndata: {\"delta\":\"chunk-2\"}",
		"id: 4\nevent: response.output_text.delta\ndata: {\"delta\":\"chunk-3-live\"}",
		"id: 5\nevent: response.completed\ndata: {\"type\":\"response.completed\"}",
		"data: [DONE]",
	)

	// Owner replaying after completion from after_seq=3 gets only seq 4-5 + [DONE].
	st, raw = doThreadReq(t, app, http.MethodGet, "/v1/responses/resp_live_1/stream?after_seq=3", aliceTok, nil)
	if st != http.StatusOK {
		t.Fatalf("alice stream replay = %d: %s", st, raw)
	}
	if strings.Contains(string(raw), "chunk-2") || !strings.Contains(string(raw), "chunk-3-live") {
		t.Fatalf("unexpected replay after_seq=3:\n%s", raw)
	}
}

func TestResponses_PreviousResponseIDContinuationAndStore(t *testing.T) {
	store := object.NewMemoryThreadStore()
	prevStore := object.DefaultThreadStore()
	object.SetDefaultThreadStore(store)
	prevHub := DefaultResponseStreamHub()
	SetDefaultResponseStreamHub(NewResponseStreamHub())
	t.Cleanup(func() {
		object.SetDefaultThreadStore(prevStore)
		SetDefaultResponseStreamHub(prevHub)
	})

	// Turn 1: store: true persists input + assistant response and indexes response_id.
	call1, err := ReadResponsesForCaller([]byte(`{
		"model": "enso-ultra",
		"instructions": "Be concise.",
		"input": "Remember secret code OMEGA-77.",
		"store": true
	}`), "", "acme", "alice", "")
	if err != nil {
		t.Fatalf("Turn 1 ReadResponsesForCaller: %v", err)
	}
	chatResp1 := `{"id":"chat_1","choices":[{"message":{"role":"assistant","content":"Noted OMEGA-77."},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14}}`
	out1, err := call1.Whole([]byte(chatResp1))
	if err != nil {
		t.Fatalf("Turn 1 Whole: %v", err)
	}
	var res1 struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(out1, &res1); err != nil || !strings.HasPrefix(res1.ID, "resp_") {
		t.Fatalf("Turn 1 response = %s", out1)
	}

	// Unauthorized org attempting to continue acme's previous_response_id MUST get ErrForbidden!
	if _, err := ReadResponsesForCaller([]byte(`{
		"model": "enso-ultra",
		"previous_response_id": "`+res1.ID+`",
		"input": "What was the code?"
	}`), "", "evil", "mallory", ""); !errors.Is(err, object.ErrForbidden) {
		t.Fatalf("cross-org ungranted continuation err = %v, want ErrForbidden", err)
	}

	// Unknown previous_response_id returns ErrPreviousResponseNotFound.
	if _, err := ReadResponsesForCaller([]byte(`{
		"model": "enso-ultra",
		"previous_response_id": "resp_does_not_exist",
		"input": "hello"
	}`), "", "acme", "alice", ""); !errors.Is(err, ErrPreviousResponseNotFound) {
		t.Fatalf("unknown previous_response_id err = %v, want ErrPreviousResponseNotFound", err)
	}

	// Turn 2: same org continues via previous_response_id — Turn 1 history is projected
	// into call2.Chat before the new user turn!
	call2, err := ReadResponsesForCaller([]byte(`{
		"model": "enso-ultra",
		"instructions": "Be concise.",
		"previous_response_id": "`+res1.ID+`",
		"input": "What was the secret code?",
		"store": true
	}`), "", "acme", "alice", "")
	if err != nil {
		t.Fatalf("Turn 2 ReadResponsesForCaller: %v", err)
	}
	var chatReq2 struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(call2.Chat, &chatReq2); err != nil {
		t.Fatalf("unmarshal chat2: %v", err)
	}
	// Expect: system("Be concise."), user("Remember secret code OMEGA-77."),
	// assistant("Noted OMEGA-77."), user("What was the secret code?")
	if len(chatReq2.Messages) != 4 {
		t.Fatalf("Turn 2 chat messages = %+v, want 4 messages (deduped system + turn1 user + turn1 assistant + turn2 user)", chatReq2.Messages)
	}
	if !strings.Contains(chatReq2.Messages[1].Content, "OMEGA-77") || !strings.Contains(chatReq2.Messages[2].Content, "Noted OMEGA-77") {
		t.Fatalf("Turn 2 missing prior turn context: %+v", chatReq2.Messages)
	}

	// Streaming Turn 2 records frames in DefaultResponseStreamHub keyed by call2.ResponseID()
	// and echoes previous_response_id in response.created/response.completed!
	streamOut := &bytes.Buffer{}
	bridge := call2.Stream(streamOut)
	upstream := strings.Join([]string{
		`data: {"choices":[{"delta":{"role":"assistant","content":"OMEGA-77"},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":22,"completion_tokens":3,"total_tokens":25}}`,
		`data: [DONE]`,
		"",
	}, "\n\n")
	if _, err := bridge.Write([]byte(upstream)); err != nil {
		t.Fatal(err)
	}
	if err := bridge.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(streamOut.String(), `"previous_response_id":"`+res1.ID+`"`) {
		t.Fatalf("stream response missing previous_response_id:\n%s", streamOut.String())
	}
	rs, ok := DefaultResponseStreamHub().Get(call2.ResponseID())
	if !ok {
		t.Fatalf("expected stream %s registered in ResponseStreamHub", call2.ResponseID())
	}
	frames, done, _ := rs.snapshotAfter(0)
	if !done || len(frames) < 3 {
		t.Fatalf("recorded stream frames = %d done=%v, want done with >=3 frames", len(frames), done)
	}
}

func TestPipeToFamily_LinkedComputeAndThreadHeadersOnlyGoToFirstParty(t *testing.T) {
	if zenPipeHeader != 600*time.Second {
		t.Fatalf("zenPipeHeader = %v, want 600s", zenPipeHeader)
	}

	store := &memSecrets{vals: map[string]string{
		"orgs/acme/users/alice/linked-compute/url":   "https://h100-pod.acme.internal/v1",
		"orgs/acme/users/alice/linked-compute/key":   "lc-secret-alice",
		"orgs/acme/users/alice/linked-compute/model": "zen5-10m-private",
	}}
	object.SetSecretStore(store)
	object.ResetProviderKeyOverrideCache()
	t.Cleanup(func() {
		object.SetSecretStore(nil)
		object.ResetProviderKeyOverrideCache()
	})
	t.Setenv("ZEN_ADMIN_TOKEN", "test-admin-tok")

	// 1. First-party Enso receives X-Hanzo-Thread, X-Hanzo-KV-Checkpoint, and KMS linked-compute headers.
	var gotThread, gotKV, gotURL, gotKey, gotModel string
	ensoSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/admin/catalog" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"models":{"enso-pro":{"route":[{"provider":"nvidia","upstream":"nvidia/nemotron","cost":{"in":"0","out":"0"}}]}}}`))
			return
		}
		gotThread = r.Header.Get("X-Hanzo-Thread")
		gotKV = r.Header.Get("X-Hanzo-KV-Checkpoint")
		gotURL = r.Header.Get("X-Hanzo-Override-Compute-Url")
		gotKey = r.Header.Get("X-Hanzo-Override-Compute-Key")
		gotModel = r.Header.Get("X-Hanzo-Override-Compute-Model")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","model":"enso-pro","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`))
	}))
	defer ensoSrv.Close()

	restore(t, ensoFam)
	ensoFam.providerFn = func() *object.Provider {
		return &object.Provider{Name: "enso", Type: "Enso", ProviderUrl: ensoSrv.URL, ClientSecret: "svc-key"}
	}
	ensoFam.mu.Lock()
	ensoFam.byID = map[string]zenModel{"enso-pro": {ID: "enso-pro"}}
	ensoFam.ids = []string{"enso-pro"}
	ensoFam.loaded = true
	ensoFam.fetchedAt = time.Now()
	ensoFam.mu.Unlock()

	// Verify 10M virtual window is advertised on first-party Enso catalog.
	listed := ensoFam.mergeModels(nil)
	if len(listed) != 1 || listed[0].VirtualContextWindow != VirtualThreadWindow {
		t.Fatalf("enso listed virtual_context_window = %+v, want %d", listed, VirtualThreadWindow)
	}

	body := []byte(`{"model":"enso-pro","stream":false,"messages":[{"role":"user","content":"hi"}]}`)
	c := visit(http.MethodPost, "/v1/chat/completions")
	c.Fiber().Request().Header.Set("X-Hanzo-Thread", "thr_abc123")
	c.Fiber().Request().Header.Set("X-Hanzo-KV-Checkpoint", "kv_ckpt_42")
	// Spoofed client compute header MUST be overwritten by KMS value, never trusted.
	c.Fiber().Request().Header.Set("X-Hanzo-Override-Compute-Url", "https://attacker.evil/v1")
	c.Fiber().Request().SetBody(body)
	user := &iam.User{Owner: "acme", Name: "alice"}
	if refused := c.pipeToFamily(ensoFam, "chat/completions", "openai", "enso-pro", body, false, 1024, "acme", user, true, nil, time.Now()); refused != nil {
		t.Fatalf("enso pipeToFamily refused: %+v", refused)
	}
	if gotThread != "thr_abc123" || gotKV != "kv_ckpt_42" {
		t.Fatalf("enso thread/kv headers = (%q, %q), want (thr_abc123, kv_ckpt_42)", gotThread, gotKV)
	}
	if gotURL != "https://h100-pod.acme.internal/v1" || gotKey != "lc-secret-alice" || gotModel != "zen5-10m-private" {
		t.Fatalf("enso linked compute headers = (%q, %q, %q)", gotURL, gotKey, gotModel)
	}

	// 2. Third-party OpenRouter MUST NEVER receive thread, KV checkpoint, or linked-compute headers!
	var leakThread, leakKV, leakURL, leakKey, leakModel string
	orSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leakThread = r.Header.Get("X-Hanzo-Thread")
		leakKV = r.Header.Get("X-Hanzo-KV-Checkpoint")
		leakURL = r.Header.Get("X-Hanzo-Override-Compute-Url")
		leakKey = r.Header.Get("X-Hanzo-Override-Compute-Key")
		leakModel = r.Header.Get("X-Hanzo-Override-Compute-Model")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-2","object":"chat.completion","model":"openai/gpt-5","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`))
	}))
	defer orSrv.Close()

	restore(t, openrouterFam)
	openrouterFam.providerFn = func() *object.Provider {
		return &object.Provider{Name: "openrouter", Type: "OpenRouter", ProviderUrl: orSrv.URL, ClientSecret: "sk-or-test"}
	}
	openrouterFam.mu.Lock()
	openrouterFam.byID = map[string]zenModel{"openai/gpt-5": {ID: "openai/gpt-5"}}
	openrouterFam.loaded = true
	openrouterFam.fetchedAt = time.Now()
	openrouterFam.mu.Unlock()

	orBody := []byte(`{"model":"openai/gpt-5","stream":false,"messages":[{"role":"user","content":"hi"}]}`)
	c2 := visit(http.MethodPost, "/v1/chat/completions")
	c2.Fiber().Request().Header.Set("X-Hanzo-Thread", "thr_abc123")
	c2.Fiber().Request().Header.Set("X-Hanzo-KV-Checkpoint", "kv_ckpt_42")
	c2.Fiber().Request().Header.Set("X-Hanzo-Override-Compute-Url", "https://attacker.evil/v1")
	c2.Fiber().Request().SetBody(orBody)
	if refused := c2.pipeToFamily(openrouterFam, "chat/completions", "openai", "openai/gpt-5", orBody, false, 1024, "acme", user, true, nil, time.Now()); refused != nil {
		t.Fatalf("openrouter pipeToFamily refused: %+v", refused)
	}
	if leakThread != "" || leakKV != "" || leakURL != "" || leakKey != "" || leakModel != "" {
		t.Fatalf("first-party headers leaked to OpenRouter: thread=%q kv=%q url=%q key=%q model=%q",
			leakThread, leakKV, leakURL, leakKey, leakModel)
	}
}
