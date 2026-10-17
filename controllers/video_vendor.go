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

// video_vendor.go — a clip a vendor makes for a caller, from create to the file.
//
// POST /v1/videos/generations for a video SKU of the resale family (Veo, Kling,
// Seedance, Wan, Hailuo, Runway, Grok Imagine, …) is held to what the model
// lists, reserved at the most the clip can cost, and sent to the vendor's async
// API (OpenRouter's POST /v1/videos) on one of our accounts. The job is filed in
// the store (object.MediaJob) and a watcher in this process drives it from there:
// it polls the vendor, and when the clip is done copies the file into the host's
// object store (Hanzo S3), reads its duration and frame size from the file, and
// bills what the vendor says the clip cost, times the markup — once, whether or
// not the caller ever asks again. A failed clip bills nothing.
//
// The caller polls GET /v1/videos/{id}, or asks it for text/event-stream and is
// told each change as it happens; a finished job carries the file's address (a
// signed URL into Hanzo S3) and GET /v1/videos/{id}/content redirects to it.
//
// A process that starts finishes every job one before it left unfinished
// (ResumeVideos): the vendor's job outlives us, and so does what it costs.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/upstream"
	"github.com/hanzoai/ai/util"
	"github.com/hanzoai/decimal"
)

// clipPoll is how often a watcher asks the vendor how a job stands.
var clipPoll = 4 * time.Second

const (
	// clipLongest is how long a job is watched before it is given up as failed.
	clipLongest = 2 * time.Hour
	// clipLink is how long a finished clip's address serves without a credential.
	clipLink = 7 * 24 * time.Hour
	// clipMost is the largest clip copied into the store.
	clipMost = 4 << 30
)

// clipJobs are the jobs this process is watching, by public id: what a request
// holds that the store cannot (the wallet hold, the paid-lane seat, the grant on
// the create's context) and the job's latest standing.
var clipJobs sync.Map // id → *clipJob

// clipJob is one watched job.
type clipJob struct {
	mu   sync.Mutex
	row  object.MediaJob
	sku  zenModel
	hold *budgetHold
	bill context.Context // the create's context without its cancellation, or nil
	seat *seat
	// changed is closed and replaced at each change of standing, so a stream
	// waiting on the job hears it.
	changed chan struct{}
}

func (j *clipJob) snapshot() object.MediaJob {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.row
}

// wait is the channel closed at the job's next change.
func (j *clipJob) wait() <-chan struct{} {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.changed
}

// set records a change of standing, files it and tells whoever waits.
func (j *clipJob) set(change func(*object.MediaJob)) {
	j.mu.Lock()
	change(&j.row)
	row := j.row
	close(j.changed)
	j.changed = make(chan struct{})
	j.mu.Unlock()
	if err := object.UpdateMediaJob(&row); err != nil {
		log.Error("video: job %s not filed: %v", row.Name, err)
	}
}

// clipRequest is a create's body as this API reads it.
type clipRequest struct {
	Model       string   `json:"model"`
	Prompt      string   `json:"prompt"`
	Seconds     int      `json:"seconds"`
	Resolution  string   `json:"resolution"`
	AspectRatio string   `json:"aspect_ratio"`
	Size        string   `json:"size"`
	Image       string   `json:"image"`      // the first frame: an https or data: URL
	LastImage   string   `json:"last_image"` // the last frame
	References  []string `json:"references"` // style or content references
	Audio       *bool    `json:"audio"`      // write audio; the model's default when absent
	Seed        *int64   `json:"seed"`
}

// ask is the request as a clip ask, before the model's defaults.
func (r clipRequest) ask() videoAsk {
	return videoAsk{
		Seconds: r.Seconds, Resolution: r.Resolution, Aspect: strings.TrimSpace(r.AspectRatio),
		Size: strings.TrimSpace(r.Size), First: strings.TrimSpace(r.Image), Last: strings.TrimSpace(r.LastImage),
		References: r.References, Audio: r.Audio, Seed: r.Seed,
	}
}

// clipFor is the video SKU a model names on the resale family, and false for any
// other model.
func clipFor(model string) (zenModel, bool) {
	if !openrouterFam.enabled() {
		return zenModel{}, false
	}
	openrouterFam.warm()
	m, ok := openrouterFam.lookup(model)
	return m, ok && m.Video != nil
}

// clipQuoteNano is the most a create's clip can cost the caller, in nano-dollars,
// and false for a body or a model it cannot bound: the paid lane's seat and the
// wallet's hold are both this.
func clipQuoteNano(model string, body []byte) (int64, bool) {
	m, ok := clipFor(model)
	if !ok {
		return 0, false
	}
	var r clipRequest
	if json.Unmarshal(body, &r) != nil {
		return 0, false
	}
	a := r.ask()
	if m.Video.admit(&a) != nil {
		return 0, false
	}
	usd, ok := m.Video.ceiling(a)
	if !ok {
		return 0, false
	}
	return nanoUp(usd.Mul(m.Margin)), true
}

// vendorClip serves a create for the video SKU m: the request is admitted against
// what the model lists, reserved, sent to the vendor, filed and watched.
func (c *ApiController) vendorClip(m zenModel, body []byte, authUser *iam.User) {
	var r clipRequest
	if err := json.Unmarshal(body, &r); err != nil {
		c.ResponseErrorWithStatus(http.StatusBadRequest, fmt.Sprintf("Failed to parse request: %s", parseProblem(err)))
		return
	}
	a := r.ask()
	if err := m.Video.admit(&a); err != nil {
		c.ResponseErrorWithStatus(http.StatusBadRequest, fmt.Sprintf("%s: %v", m.ID, err))
		return
	}
	usd, ok := m.Video.ceiling(a)
	if !ok {
		c.ResponseErrorWithStatus(http.StatusServiceUnavailable, fmt.Sprintf("model %q states no price that bounds a clip, so it is not served", m.ID))
		return
	}
	quote := nanoUp(usd.Mul(m.Margin))
	if object.AssetStore() == nil {
		c.ResponseErrorWithStatus(http.StatusServiceUnavailable, "video generation has nowhere to keep its files on this deployment")
		return
	}
	ledger := c.billingOrg(authUser)
	subject := authUser.PayerSubject(ledger)
	hold, okHold := reserveFor(c.Context(), subject, centsUp(quote))
	if !okHold {
		c.ResponseAuthError(billingError("%s", object.InsufficientBalance(c.Host(), ledger, "video cost").Message))
		return
	}

	out, err := a.vendorBody(m.ID, r.Prompt)
	if err != nil {
		hold.settle(0)
		c.ResponseError(err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(c.Context(), 60*time.Second)
	defer cancel()
	status, said, account, err := clipCall(ctx, http.MethodPost, "/v1/videos", out, "")
	if err != nil || status/100 != 2 {
		hold.settle(0)
		c.clipRefused(m.ID, status, said, err)
		return
	}
	var made struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if json.Unmarshal(said, &made) != nil || made.ID == "" {
		hold.settle(0)
		c.ResponseErrorWithStatus(http.StatusBadGateway, fmt.Sprintf("model %q: the vendor accepted the clip without naming its job", m.ID))
		return
	}

	audio := a.Audio == nil || *a.Audio
	if m.Video.Audio != nil && !*m.Video.Audio {
		audio = false
	}
	j := &clipJob{
		row: object.MediaJob{
			Owner: ledger, Name: "video_" + uuid.NewString(), CreatedTime: util.GetCurrentTime(),
			Model: m.ID, Vendor: openrouterFam.name, Upstream: made.ID, Account: account,
			Subject: subject, Organization: authUser.Owner, User: authUser.Name, UserType: authUser.Type,
			BillingAccount: authUser.BillingAccount,
			Status:         normalizeVideoStatus(made.Status), Seconds: a.Seconds, Resolution: a.Resolution,
			Size: a.Size, Aspect: a.Aspect, Audio: audio, Quote: nanoUSD(&quote),
		},
		sku: m, hold: hold, bill: billing(c.Context()), seat: seatOf(c.Context()), changed: make(chan struct{}),
	}
	if err := object.AddMediaJob(&j.row); err != nil {
		// The vendor is making a clip nobody can be billed for or handed. Say so;
		// the next process cannot finish what is not filed.
		hold.settle(0)
		log.Error("video: job for %s upstream=%s not filed: %v", m.ID, made.ID, err)
		c.ResponseErrorWithStatus(http.StatusInternalServerError, "the video job could not be filed")
		return
	}
	j.seat.keep()
	clipJobs.Store(j.row.Name, j)
	go j.watch()
	c.jsonResponse(clipAnswer(context.Background(), j.snapshot()))
}

// clipRefused answers a vendor that would not take a clip: a refusal of the
// request is the caller's to read; an account we could not pay with, or a vendor
// that failed, is ours, and never reads as their debt.
func (c *ApiController) clipRefused(model string, status int, said []byte, err error) {
	switch {
	case err != nil:
		c.ResponseErrorWithStatus(http.StatusServiceUnavailable, fmt.Sprintf("model %q is temporarily unavailable; try again shortly", model))
	case status == http.StatusBadRequest || status == http.StatusUnprocessableEntity:
		c.ResponseErrorWithStatus(http.StatusBadRequest, fmt.Sprintf("model %q refused the request: %s", model, upstreamErrorMessage(said)))
	default:
		c.ResponseFailure(&apiError{status: http.StatusServiceUnavailable, code: codeExhausted,
			msg: fmt.Sprintf("model %q is temporarily unavailable; try again shortly", model)})
	}
}

// clipCall is one request to the resale vendor's video API on our accounts: the
// account named, or, for a new job, each account in turn until one is not
// refused for want of credit or as a credential. It answers the status, the body
// and the name of the account that answered.
func clipCall(ctx context.Context, method, path string, body []byte, account string) (int, []byte, string, error) {
	p := openrouterFam.provider()
	if p == nil {
		return 0, nil, "", errors.New("the video vendor is not configured")
	}
	type key struct{ name, value string }
	var keys []key
	if account != "" {
		keys = []key{{account, object.ResolveKey(account)}}
	} else {
		values := openrouterFam.keys()
		for _, v := range order(values, false) {
			if !cooling(v, false) {
				keys = append(keys, key{object.AccountOf(openrouterFam.keyNames, v), v})
			}
		}
		if len(values) == 0 && strings.TrimSpace(p.ClientSecret) != "" {
			keys = []key{{p.Name, p.ClientSecret}}
		}
	}
	if len(keys) == 0 {
		return 0, nil, "", errors.New("no account with the video vendor can pay")
	}
	base := strings.TrimRight(strings.TrimSpace(p.ProviderUrl), "/")
	var lastStatus int
	var last []byte
	for _, k := range keys {
		req, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(body))
		if err != nil {
			return 0, nil, "", err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		pk := *p
		pk.ClientSecret = k.value
		upstream.Authorize(req, &pk)
		resp, err := zenPipeClient.Do(req)
		if err != nil {
			return 0, nil, "", err
		}
		said, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusPaymentRequired:
			cool(k.value, scopePriced)
			emptied(openrouterFam.name, k.name, "", resp.StatusCode)
		case http.StatusUnauthorized:
			cool(k.value, scopeAll)
		default:
			return resp.StatusCode, said, k.name, nil
		}
		lastStatus, last = resp.StatusCode, said
		if account != "" {
			break
		}
	}
	return lastStatus, last, "", nil
}

// vendorJob is the vendor's word on a job (GET /v1/videos/{id}).
type vendorJob struct {
	Status   string   `json:"status"`
	Unsigned []string `json:"unsigned_urls"`
	Usage    *struct {
		Cost json.Number `json:"cost"`
	} `json:"usage"`
	Error any `json:"error"`
}

// watch drives a job to its end: it asks the vendor how the job stands until it
// is done or failed, then keeps the file and bills it, or releases what it held.
func (j *clipJob) watch() {
	row := j.snapshot()
	created, _ := time.Parse(time.RFC3339, row.CreatedTime)
	if created.IsZero() {
		created = time.Now()
	}
	for {
		if time.Since(created) > clipLongest {
			j.fail("the clip did not finish within " + clipLongest.String())
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		status, said, _, err := clipCall(ctx, http.MethodGet, "/v1/videos/"+row.Upstream, nil, row.Account)
		cancel()
		var v vendorJob
		switch {
		case err != nil || status >= 500 || status == http.StatusTooManyRequests:
			// The vendor could not say: ask again.
		case status != http.StatusOK || json.Unmarshal(said, &v) != nil:
			j.fail(fmt.Sprintf("the vendor could not say how the clip stands (%d)", status))
			return
		default:
			switch s := normalizeVideoStatus(v.Status); s {
			case "completed":
				j.finish(v)
				return
			case "failed", "expired":
				j.fail(vendorFailure(v.Error))
				return
			default:
				if s != j.snapshot().Status {
					j.set(func(r *object.MediaJob) { r.Status = s })
				}
			}
		}
		time.Sleep(clipPoll)
	}
}

// vendorFailure is a failed job's reason as a caller may read it.
func vendorFailure(e any) string {
	switch v := e.(type) {
	case string:
		if s := sayable(v); s != "" {
			return s
		}
	case map[string]any:
		if m, ok := v["message"].(string); ok {
			if s := sayable(m); s != "" {
				return s
			}
		}
	}
	return "the vendor could not make the clip"
}

// fail ends a job that made nothing: it bills nothing and gives back what it held.
func (j *clipJob) fail(why string) {
	j.hold.settle(0)
	j.seat.settle()
	j.set(func(r *object.MediaJob) {
		r.Status, r.Error, r.Settled = "failed", why, true
		r.FinishedTime = util.GetCurrentTime()
	})
	clipJobs.Delete(j.snapshot().Name)
	log.Warn("video: job %s failed: %s", j.snapshot().Name, why)
}

// finish keeps a done clip's file, reads it, and bills it.
func (j *clipJob) finish(v vendorJob) {
	row := j.snapshot()
	key := "videos/" + row.Owner + "/" + row.Name + ".mp4"
	file, err := j.fetch(v, key)
	if err != nil {
		// The vendor made it and bills us for it; the caller is billed too, for a
		// clip we could not keep would still be theirs to ask for again. Say why.
		log.Error("video: job %s: the clip could not be kept: %v", row.Name, err)
	}
	cost, stated := statedCost(v)
	billed := nanoFrom(row.Quote)
	if stated != nil {
		billed = j.sku.bill(stated, tokens{})
		if q := nanoFrom(row.Quote); billed > q {
			log.Warn("video: job %s cost more than it reserved (%d > %d nano): the quote is low for %s", row.Name, billed, q, row.Model)
		}
	}
	j.hold.settleNano(billed)
	j.record(stated, billed)
	j.set(func(r *object.MediaJob) {
		r.Status = "completed"
		r.Settled = true
		r.Cost, r.Billed = cost, nanoUSD(&billed)
		r.FinishedTime = util.GetCurrentTime()
		if err == nil {
			r.Asset, r.Bytes, r.Width, r.Height, r.Duration = key, file.bytes, file.width, file.height, file.seconds
		} else {
			r.Error = "the clip was made but could not be kept"
		}
	})
	clipJobs.Delete(row.Name)
}

// statedCost is what the vendor said a job cost: its USD string and, in
// nano-dollars, the cost stated; nil when it stated none.
func statedCost(v vendorJob) (string, *int64) {
	if v.Usage == nil || v.Usage.Cost == "" {
		return "", nil
	}
	d, err := decimal.Parse(v.Usage.Cost.String())
	if err != nil || d.Sign() <= 0 {
		return "", nil
	}
	n := nanoUp(d)
	return d.String(), &n
}

// clipFile is a kept clip as read from its file.
type clipFile struct {
	bytes         int64
	width, height int
	seconds       float64
}

// fetch copies a done clip from the vendor into the host's store under key and
// reads its size, duration and frame size.
func (j *clipJob) fetch(v vendorJob, key string) (clipFile, error) {
	row := j.snapshot()
	p := openrouterFam.provider()
	if p == nil {
		return clipFile{}, errors.New("the video vendor is not configured")
	}
	src := strings.TrimRight(strings.TrimSpace(p.ProviderUrl), "/") + "/v1/videos/" + row.Upstream + "/content?index=0"
	if len(v.Unsigned) > 0 && strings.HasPrefix(v.Unsigned[0], "https://") {
		src = v.Unsigned[0]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return clipFile{}, err
	}
	pk := *p
	pk.ClientSecret = object.ResolveKey(row.Account)
	upstream.Authorize(req, &pk)
	resp, err := zenPipeClient.Do(req)
	if err != nil {
		return clipFile{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return clipFile{}, fmt.Errorf("the vendor answered %d for the file", resp.StatusCode)
	}
	tmp, err := os.CreateTemp("", "clip-*.mp4")
	if err != nil {
		return clipFile{}, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	n, err := io.Copy(tmp, io.LimitReader(resp.Body, clipMost+1))
	if err != nil {
		return clipFile{}, err
	}
	if n > clipMost {
		return clipFile{}, fmt.Errorf("the clip is larger than %d bytes", int64(clipMost))
	}
	f := clipFile{bytes: n}
	if info, err := readMP4(tmp, n); err == nil {
		f.width, f.height, f.seconds = info.width, info.height, info.seconds
	} else {
		log.Warn("video: job %s: the file did not read as MP4: %v", row.Name, err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return clipFile{}, err
	}
	kind := resp.Header.Get("Content-Type")
	if kind == "" || !strings.HasPrefix(kind, "video/") {
		kind = "video/mp4"
	}
	if err := object.AssetStore().Put(ctx, key, kind, tmp, n); err != nil {
		return clipFile{}, err
	}
	return f, nil
}

// record files the job's one usage row: the caller's clip at what it billed and
// what it cost us, on the create's context (its grant and seat) when this process
// made it, else on the caller's wallet as prepaid cash.
func (j *clipJob) record(stated *int64, billed int64) {
	row := j.snapshot()
	u := &iam.User{Owner: row.Organization, Name: row.User, Type: row.UserType, BillingAccount: row.BillingAccount}
	rec := &usageRecord{
		Owner: row.Owner, Organization: row.Organization, Model: row.Model,
		Provider: openrouterFam.name, Origin: "openrouter.ai", VideoCount: 1, Currency: "USD", Premium: true,
		Status: "success", RequestID: row.Name, BilledNanoExact: &billed,
		CostNanoExact: costed(stated, openrouterFam.name),
	}
	ctx := j.bill
	if ctx == nil {
		ctx = context.Background()
		rec.cash = true
	}
	rec.bind(ctx, u)
	if err := recordUsage(rec); err != nil {
		log.Error("video: job %s billed %d nano was not recorded: %v", row.Name, billed, err)
	}
	created, _ := time.Parse(time.RFC3339, row.CreatedTime)
	recordTrace(ctx, rec, created)
}

// nanoFrom reads a USD decimal string as nano-dollars, rounded up.
func nanoFrom(usd string) int64 {
	d, err := decimal.Parse(usd)
	if err != nil {
		return 0
	}
	return nanoUp(d)
}

// ResumeVideos watches every job a process before this one left unfinished, so
// each is kept and billed. Called once at start.
func ResumeVideos() {
	rows, err := object.UnsettledMediaJobs()
	if err != nil {
		log.Warn("video: unfinished jobs not read: %v", err)
		return
	}
	for _, r := range rows {
		if _, watched := clipJobs.Load(r.Name); watched {
			continue
		}
		m, ok := clipFor(r.Model)
		if !ok {
			// The model is no longer listed, so its price cannot be read. The clip
			// is still the vendor's to finish and ours to pay for; bill the quote.
			m = zenModel{ID: r.Model, Margin: decimal.New(1, 0), Variable: true}
		}
		j := &clipJob{row: *r, sku: m, changed: make(chan struct{})}
		clipJobs.Store(r.Name, j)
		go j.watch()
		log.Info("video: resumed job %s (%s, %s)", r.Name, r.Model, r.Status)
	}
}

// clipStatus is a job as /v1/videos answers it: the OpenAI video object, with what
// a finished clip is — its address, size, duration and frame size — and what it
// billed.
type clipStatus struct {
	ID          string        `json:"id"`
	Object      string        `json:"object"`
	Model       string        `json:"model"`
	Status      string        `json:"status"`
	CreatedAt   int64         `json:"created_at"`
	CompletedAt int64         `json:"completed_at,omitempty"`
	Progress    int           `json:"progress"`
	Seconds     int           `json:"seconds"`
	Resolution  string        `json:"resolution,omitempty"`
	Size        string        `json:"size,omitempty"`
	AspectRatio string        `json:"aspect_ratio,omitempty"`
	Audio       bool          `json:"audio"`
	URL         string        `json:"url,omitempty"`
	ExpiresAt   int64         `json:"expires_at,omitempty"`
	ContentType string        `json:"content_type,omitempty"`
	Bytes       int64         `json:"bytes,omitempty"`
	Width       int           `json:"width,omitempty"`
	Height      int           `json:"height,omitempty"`
	Duration    float64       `json:"duration,omitempty"`
	Cost        *clipCost     `json:"cost,omitempty"`
	Error       *videoFailure `json:"error,omitempty"`
}

// clipCost is what a job reserved and, once it finished, what it billed, in USD.
type clipCost struct {
	Reserved string `json:"reserved"`
	Billed   string `json:"billed,omitempty"`
	Currency string `json:"currency"`
}

// clipAnswer projects a job for the caller. It never names the vendor's job, the
// account or the key.
func clipAnswer(ctx context.Context, r object.MediaJob) clipStatus {
	created, _ := time.Parse(time.RFC3339, r.CreatedTime)
	out := clipStatus{
		ID: r.Name, Object: "video", Model: r.Model, Status: r.Status, CreatedAt: created.Unix(),
		Seconds: r.Seconds, Resolution: r.Resolution, Size: r.Size, AspectRatio: r.Aspect, Audio: r.Audio,
		Cost: &clipCost{Reserved: r.Quote, Billed: r.Billed, Currency: "usd"},
	}
	switch r.Status {
	case "completed":
		out.Progress = 100
		if done, err := time.Parse(time.RFC3339, r.FinishedTime); err == nil {
			out.CompletedAt = done.Unix()
		}
		if r.Asset != "" {
			if a := object.AssetStore(); a != nil {
				if u, exp, err := a.URL(ctx, r.Asset, clipLink); err == nil {
					out.URL, out.ExpiresAt, out.ContentType = u, exp.Unix(), "video/mp4"
				}
			}
			out.Bytes, out.Width, out.Height, out.Duration = r.Bytes, r.Width, r.Height, r.Duration
		}
		if r.Error != "" {
			out.Error = &videoFailure{Message: r.Error}
		}
	case "failed":
		reason := r.Error
		if reason == "" {
			reason = "Video generation failed."
		}
		out.Error = &videoFailure{Message: reason}
	case "in_progress":
		out.Progress = 50
	}
	return out
}

// clipOwned is the job id names when the request's caller owns it: its billing
// subject is the job's. A job that is not there and one that is someone else's
// are the same 404, so a caller cannot learn another's job exists. handled is
// false for an id that is no vendor job, which the older path answers.
func (c *ApiController) clipOwned(token, id string) (row *object.MediaJob, job *clipJob, handled bool) {
	if v, ok := clipJobs.Load(id); ok {
		job = v.(*clipJob)
		r := job.snapshot()
		row = &r
	} else if r, err := object.MediaJobNamed(id); err == nil && r != nil {
		row = r
	} else {
		return nil, nil, false
	}
	if isPublishableKey(token) {
		c.rejectPublishableKey()
		return nil, nil, true
	}
	_, authUser, _, _, err := c.authResolveProvider(token, row.Model, c.GetOrg())
	if err != nil {
		c.ResponseAuthError(err)
		return nil, nil, true
	}
	subject := ""
	if authUser != nil {
		subject = authUser.PayerSubject(c.billingOrg(authUser))
	}
	if subject == "" || subject != row.Subject {
		c.ResponseErrorWithStatus(http.StatusNotFound, "No such video job.")
		return nil, nil, true
	}
	return row, job, true
}

// answerClip answers GET /v1/videos/{id} for a vendor job: the job as it stands,
// or, to a caller that asks for text/event-stream, each change of it as it
// happens until it ends. false for an id that is no vendor job.
func (c *ApiController) answerClip(token, id string) bool {
	row, job, handled := c.clipOwned(token, id)
	if !handled || row == nil {
		return handled
	}
	if !strings.Contains(c.Header("Accept"), "text/event-stream") {
		c.jsonResponse(clipAnswer(c.Context(), *row))
		return true
	}
	// What the stream reads, read now: it is written after this handler returns.
	name, first := row.Name, *row
	c.SetHeader("Content-Type", "text/event-stream")
	c.SetHeader("Cache-Control", "no-store")
	c.SetHeader("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	_ = c.SendStreamWriter(func(w *bufio.Writer) {
		streamClip(w, name, first, job)
	})
	return true
}

// streamClip writes a job's standing as `event: video` frames: once now, again at
// each change, and a comment every 15 seconds between, until the job ends or the
// reader leaves.
func streamClip(w *bufio.Writer, name string, row object.MediaJob, job *clipJob) {
	send := func(r object.MediaJob) bool {
		b, _ := json.Marshal(clipAnswer(context.Background(), r))
		if _, err := fmt.Fprintf(w, "event: video\ndata: %s\n\n", b); err != nil {
			return false
		}
		return w.Flush() == nil
	}
	if !send(row) {
		return
	}
	beat := time.NewTicker(15 * time.Second)
	defer beat.Stop()
	deadline := time.After(clipLongest)
	for !isTerminalVideoStatus(row.Status) {
		// A job this process watches says when it changes; one it does not is read
		// from the store again at the watcher's pace.
		var changed <-chan struct{}
		var reread <-chan time.Time
		if job != nil {
			changed = job.wait()
		} else {
			reread = time.After(clipPoll)
		}
		select {
		case <-changed:
			row = job.snapshot()
			if !send(row) {
				return
			}
		case <-reread:
			r, err := object.MediaJobNamed(name)
			if err != nil || r == nil || r.Status == row.Status {
				continue
			}
			row = *r
			if !send(row) {
				return
			}
		case <-beat.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil || w.Flush() != nil {
				return
			}
		case <-deadline:
			return
		}
	}
}

// clipContent answers GET /v1/videos/{id}/content for a vendor job: a redirect to
// the kept file. false for an id that is no vendor job.
func (c *ApiController) clipContent(token, id string) bool {
	row, _, handled := c.clipOwned(token, id)
	if !handled || row == nil {
		return handled
	}
	if row.Status != "completed" || row.Asset == "" {
		c.ResponseErrorWithStatus(http.StatusConflict, "The video is not ready.")
		return true
	}
	a := object.AssetStore()
	if a == nil {
		c.ResponseErrorWithStatus(http.StatusServiceUnavailable, "video files are not served on this deployment")
		return true
	}
	u, _, err := a.URL(c.Context(), row.Asset, clipLink)
	if err != nil {
		c.ResponseErrorWithStatus(http.StatusServiceUnavailable, "the video file could not be addressed")
		return true
	}
	c.SetHeader("Location", u)
	c.SetHeader("Cache-Control", "no-store")
	c.Status(http.StatusFound)
	return true
}
