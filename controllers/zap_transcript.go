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

// The growing transcript — /v1/audio/transcriptions' streaming sibling.
//
//	POST   /v1/audio/transcript        {model, language}  -> {id, chunk_ms, …}
//	POST   /v1/audio/transcript/{tid}  <raw pcm16>        -> {text, pending, seconds}
//	DELETE /v1/audio/transcript/{tid}                     -> the settled text
//
// HTTP-SHAPED, not body-only, and it has to be: the session id is in the PATH
// (there is no room for it in a body that is raw audio), and push and close share
// that path and differ only by method. registerGatewayPath hands a handler the
// body and nothing else, so a body-only registration cannot see either.
//
// TWO THINGS THIS FILE OWNS, and they are the reasons it exists rather than the
// gateway simply forwarding:
//
//  1. WHICH PROCESS HOLDS THE SESSION. A growing transcript is a window in one
//     speech replica's memory. speech.hanzo.svc is a ClusterIP, and kube-proxy
//     picks a backend PER CONNECTION — so with two replicas roughly half of a
//     session's pushes reach a pod that has never heard of it and answer 404,
//     and a client with a connection pool sees it intermittently. `open` answers
//     with the address of the pod that took the session ("at"), and every later
//     call for that session goes there. A pod that has gone is a session that has
//     gone, which is honest: the window was only ever in its memory.
//
//  2. WHAT IT COST. Audio is metered in seconds and the ledger lives here, not in
//     speech. The quantity is the DELTA OF `seconds`, the cumulative total the
//     session reports — never the per-push `duration`. Two reasons, and both are
//     about not losing money quietly: a cumulative delta has bounded total error
//     (only the last reading's rounding survives) where a sum of per-push values
//     accumulates every one of them; and if one push's response is lost in
//     flight, the next delta covers the gap, where a sum loses that audio for
//     good.
package controllers

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/hanzoai/account"
	"github.com/hanzoai/ai/object"
	"github.com/luxfi/zap"
)

const transcriptPath = "/v1/audio/transcript"

// transcriptCeiling is the largest push this door forwards, matching the ceiling
// speech itself enforces (transcript.CEILING = 2 s of pcm16). Checked here as
// well because a body that is refused upstream still crossed the wire, and this
// is the door that is reachable from outside the cluster.
const transcriptCeiling = 64 * 1024

// transcriptIdle is how long an untouched session is kept. speech collects its
// own at the same age, so a record outliving the window it names would only ever
// be a way to forward a push to a session that is gone.
const transcriptIdle = 30 * time.Second

func init() {
	registerGatewayRoute(transcriptPath, zapTranscriptHandler)
	registerCloud("audio.transcript", zapTranscriptCloudHandler)
}

// session is one open transcript: who pays for it, where it lives, and how much
// of it has already been billed.
type session struct {
	org      string
	user     string
	model    string
	premium  bool
	provider string // provider row name, for the usage record
	// payer is the money address this session spends from, resolved ONCE at open.
	// A credential's payer is a property of the credential, not of the chunk it
	// happens to be pushing, and re-resolving it per push is how a long session
	// could start billing a different ledger halfway through.
	payer    account.Account
	at       string // the speech pod that holds the window
	upstream string // the id THAT pod knows the session by
	metered  float64
	touched  time.Time
	// release returns the admission slot this session holds. A live transcript is
	// CONTINUOUS load — one open session keeps a decode worker busy for as long as
	// the meeting lasts — so the slot is taken for the session, not for a push. A
	// ceiling applied per push would admit any number of meetings and then refuse
	// chunks in the middle of them, which is the same capacity spent to produce a
	// worse answer.
	release func()
}

// live is this process's open sessions. In-process because the record IS the
// routing decision and the meter's running total, and both are worthless to a
// process that did not open the session — there is nothing here another replica
// could usefully read.
//
// `here` is what makes that legible instead of mysterious. Every id this door
// hands out names the process that minted it, so an id presented to a DIFFERENT
// instance is answered "opened by another instance" rather than "no such
// transcript" — the same 404 an expired session gets, which is exactly the
// confusion that would send someone hunting the wrong bug.
var (
	liveMu sync.Mutex
	live   = map[string]*session{}
	here   = mintNonce()
)

func mintNonce() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// zapTranscriptHandler serves the three calls. It declines nothing: the whole
// /v1/audio/transcript subtree is this file's.
func zapTranscriptHandler(ctx context.Context, method, path, query, auth string, body []byte) (*zap.Message, error) {
	if auth == "" {
		return object.BuildCloudResponse(401, nil, "authentication required")
	}
	tid := strings.Trim(strings.TrimPrefix(path, transcriptPath), "/")
	switch {
	case tid == "" && strings.EqualFold(method, http.MethodPost):
		return transcriptOpen(ctx, auth, body)
	case tid != "" && strings.EqualFold(method, http.MethodPost):
		return transcriptPush(ctx, auth, tid, body)
	case tid != "" && strings.EqualFold(method, http.MethodDelete):
		return transcriptClose(ctx, auth, tid)
	}
	return object.BuildCloudResponse(405, nil, "the transcript endpoint takes POST to open and push, DELETE to close")
}

// zapTranscriptCloudHandler is the native-cloud (MsgType 100) door onto the same
// three calls. A method name is not a URL, so the call it means travels in the
// body alongside its arguments.
func zapTranscriptCloudHandler(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	var in struct {
		ID    string `json:"id"`
		Close bool   `json:"close"`
		Audio []byte `json:"audio"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return object.BuildCloudResponse(400, nil, "invalid request: "+err.Error())
	}
	switch {
	case in.ID == "":
		return transcriptOpen(ctx, auth, body)
	case in.Close:
		return transcriptClose(ctx, auth, in.ID)
	default:
		return transcriptPush(ctx, auth, in.ID, in.Audio)
	}
}

// transcriptOpen resolves the caller, opens a window on ONE speech pod, and
// records where it lives.
func transcriptOpen(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	var req struct {
		Model    string `json:"model"`
		Language string `json:"language"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return object.BuildCloudResponse(400, nil, "invalid request: "+err.Error())
	}
	if req.Model == "" {
		return object.BuildCloudResponse(400, nil, "transcript request requires a \"model\" field")
	}
	provider, authUser, upstreamModel, err := zapResolveAuth(auth, req.Model)
	if err != nil {
		return object.BuildCloudResponse(401, nil, err.Error())
	}
	// The SAME prepaid gate every other audio door takes. It runs at OPEN, so a
	// caller with no balance is refused before a window exists to push into —
	// the alternative is discovering it 250 ms at a time.
	if gateErr := enforceBalanceGate(authUser, "", req.Model); gateErr != nil {
		return object.BuildCloudResponse(uint32(statusOf(gateErr)), nil, gateErr.Error())
	}
	if provider.Type == "Zen" {
		return object.BuildCloudResponse(400, nil, "model \""+req.Model+"\" does not serve the /v1/audio/transcript endpoint")
	}
	release, refused := admitSpeech(authUser.Owner)
	if refused != nil {
		return object.BuildCloudResponse(uint32(statusOf(refused)), nil, refused.Error())
	}
	if err := object.ResolveProviderSecret(provider); err != nil {
		return object.BuildCloudResponse(502, nil, "provider secret: "+err.Error())
	}

	model := upstreamModel
	if model == "" {
		model = req.Model
	}
	ask, _ := json.Marshal(map[string]any{"model": model, "language": req.Language})
	base := strings.TrimSuffix(provider.ProviderUrl, "/")
	status, opened, err := transcriptCall(ctx, http.MethodPost, base+"/audio/transcript", provider.ClientSecret, ask, "application/json")
	// Every refusal from here on gives the slot back. A window that was never
	// opened holds no capacity, and a slot kept for one would shrink the ceiling
	// by one for the life of the process.
	if err != nil {
		release()
		return object.BuildCloudResponse(502, nil, "open transcript: "+err.Error())
	}
	if status != http.StatusCreated && status != http.StatusOK {
		release()
		return object.BuildCloudResponse(uint32(status), nil, upstreamErrorMessage(opened))
	}
	var out map[string]any
	if err := json.Unmarshal(opened, &out); err != nil {
		release()
		return object.BuildCloudResponse(502, nil, "open transcript: upstream answered "+err.Error())
	}
	upstream, _ := out["id"].(string)
	if upstream == "" {
		release()
		return object.BuildCloudResponse(502, nil, "open transcript: upstream named no session")
	}
	at, _ := out["at"].(string)
	at = pinned(at, base)

	id := "ats_" + here + "_" + uuid.NewString()
	isPremium := false
	if route := resolveModelRoute(req.Model); route != nil {
		isPremium = route.premium
	}
	liveMu.Lock()
	sweepTranscripts(time.Now())
	live[id] = &session{
		org: authUser.Owner, user: authUser.Owner + "/" + authUser.Name,
		model: req.Model, premium: isPremium, provider: provider.Name,
		payer: authUser.Payer(authUser.Owner),
		at:    at, upstream: upstream, touched: time.Now(), release: release,
	}
	liveMu.Unlock()

	// The caller is handed OUR id and never the upstream's: the upstream id names
	// a session on a pod, and the pod is not the caller's business.
	out["id"] = id
	delete(out, "at")
	answer, _ := json.Marshal(out)
	return object.BuildCloudResponse(201, answer, "")
}

// pinned is the address the rest of a session goes to: the pod `open` named, or
// the address already in hand when it named none.
//
// Empty is not a failure. A single upstream process has nothing to pin to, and
// the Service address is then exactly right — so the honest answer is to keep it,
// not to guess a host. Above one replica it IS a failure, and it is the
// deployment's: speech reads its own address from the downward API, and the
// manifest that omits it is what this cannot fix from here.
func pinned(at, base string) string {
	if at == "" {
		return base
	}
	return strings.TrimSuffix(at, "/") + "/v1"
}

// transcriptPush forwards audio to the pod holding the window and meters the
// audio the answer says has now been received.
func transcriptPush(ctx context.Context, auth string, tid string, pcm []byte) (*zap.Message, error) {
	if len(pcm) > transcriptCeiling {
		return object.BuildCloudResponse(413, nil, fmt.Sprintf("chunk is %d bytes; limit %d", len(pcm), transcriptCeiling))
	}
	s, msg := holder(auth, tid)
	if msg != nil {
		return msg, nil
	}
	start := time.Now().UTC()
	status, answer, err := transcriptCall(ctx, http.MethodPost, s.at+"/audio/transcript/"+s.upstream, "", pcm, "application/octet-stream")
	if err != nil {
		return object.BuildCloudResponse(502, nil, "push: "+err.Error())
	}
	if status != http.StatusOK {
		// The window is gone upstream (expired, or its pod restarted), so the
		// record here names nothing. Drop it rather than leave a session that can
		// only ever fail.
		if status == http.StatusNotFound {
			forget(tid)
		}
		return object.BuildCloudResponse(uint32(status), nil, upstreamErrorMessage(answer))
	}
	return object.BuildCloudResponse(200, meterTranscript(ctx, s, tid, answer, start), "")
}

// transcriptClose settles the transcript and forgets it. Closing carries no
// audio, but it is metered the same way for the same reason: `seconds` is the
// truth, and the last push's answer may never have arrived.
func transcriptClose(ctx context.Context, auth string, tid string) (*zap.Message, error) {
	s, msg := holder(auth, tid)
	if msg != nil {
		return msg, nil
	}
	start := time.Now().UTC()
	status, answer, err := transcriptCall(ctx, http.MethodDelete, s.at+"/audio/transcript/"+s.upstream, "", nil, "")
	forget(tid)
	if err != nil {
		return object.BuildCloudResponse(502, nil, "close: "+err.Error())
	}
	if status != http.StatusOK {
		return object.BuildCloudResponse(uint32(status), nil, upstreamErrorMessage(answer))
	}
	return object.BuildCloudResponse(200, meterTranscript(ctx, s, tid, answer, start), "")
}

// meterTranscript debits the audio that arrived since this session was last
// metered and returns the answer with the upstream's id replaced by ours.
//
// The delta is clamped at zero: `seconds` only grows, so a smaller reading is an
// upstream that has been restarted under the same id, and a negative debit is a
// refund nobody asked for.
func meterTranscript(ctx context.Context, s *session, tid string, answer []byte, start time.Time) []byte {
	var state map[string]any
	if err := json.Unmarshal(answer, &state); err != nil {
		return answer
	}
	total, _ := state["seconds"].(float64)

	liveMu.Lock()
	delta := total - s.metered
	if delta < 0 {
		delta = 0
	}
	s.metered = total
	s.touched = time.Now()
	liveMu.Unlock()

	if delta > 0 {
		rec := &usageRecord{
			Owner: s.org, User: s.user, Organization: s.org,
			Model: s.model, Provider: s.provider, Currency: "USD",
			Premium: s.premium, Status: "success",
			AudioSeconds: delta,
			RequestID:    uuid.NewString(),
			Payer:        s.payer,
		}
		recordUsage(rec)
		recordTrace(ctx, rec, start)
	}

	state["id"] = tid
	// `duration` is the upstream's per-push figure, and it is NOT what was
	// metered: it is rounded to a millisecond, so a sum of it drifts, and a push
	// under 14 bytes reports zero. The answer carries what this call actually
	// billed instead, so the caller and the ledger cannot disagree.
	state["duration"] = delta
	out, err := json.Marshal(state)
	if err != nil {
		return answer
	}
	return out
}

// holder resolves the session a call names, and refuses in a way that says which
// kind of miss it was.
//
// The ORG is checked, not merely the id: an id is a bearer of nothing, and a
// session belongs to the tenant that opened it. Without this a guessed id would
// let one tenant push audio into another's meter — and read their transcript.
func holder(auth string, tid string) (*session, *zap.Message) {
	user, err := zapResolveUser(auth)
	if err != nil {
		msg, _ := object.BuildCloudResponse(401, nil, err.Error())
		return nil, msg
	}
	owner := orgOf(user)

	liveMu.Lock()
	defer liveMu.Unlock()
	s, ok := live[tid]
	if !ok {
		if parts := strings.SplitN(tid, "_", 3); len(parts) == 3 && parts[0] == "ats" && parts[1] != here {
			msg, _ := object.BuildCloudResponse(409, nil, "this transcript was opened by another instance; open a new one")
			return nil, msg
		}
		msg, _ := object.BuildCloudResponse(404, nil, "no transcript "+tid)
		return nil, msg
	}
	if s.org != owner {
		// Deliberately the same answer a missing session gets: whether an id
		// exists is not a fact another tenant is entitled to.
		msg, _ := object.BuildCloudResponse(404, nil, "no transcript "+tid)
		return nil, msg
	}
	s.touched = time.Now()
	return s, nil
}

func forget(tid string) {
	liveMu.Lock()
	if s, ok := live[tid]; ok {
		delete(live, tid)
		s.release()
	}
	liveMu.Unlock()
}

// sweepTranscripts drops records for sessions speech has already collected.
// Called from open, under the lock, for the reason speech sweeps there too:
// there is then no clock to run and no task to supervise.
func sweepTranscripts(now time.Time) {
	for id, s := range live {
		if now.Sub(s.touched) > transcriptIdle {
			delete(live, id)
			// A session abandoned mid-call — the client went away without closing —
			// still holds a slot until this runs. Sweeping is the only thing that
			// gives it back, which is why the sweep is not optional.
			s.release()
		}
	}
}

// transcriptClient is separate from the chat and zen pools: a push is small and
// frequent and must not wait behind a minutes-long completion, and its timeout is
// the ack's, not a generation's.
var transcriptClient = &http.Client{Timeout: 30 * time.Second}

func transcriptCall(ctx context.Context, method, url, secret string, body []byte, ctype string) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return 0, nil, err
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := transcriptClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	answer, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, answer, nil
}
