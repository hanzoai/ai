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
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/decimal"
)

// videoListing is four entries of OpenRouter's GET /v1/videos/models as listed on
// 2026-10-16, and one text model beside them.
const videoListing = `{"data":[
{"id":"google/veo-3.1","name":"Google: Veo 3.1","created":1774277148,"description":"Veo","supported_resolutions":["720p","1080p","4K"],"supported_aspect_ratios":["16:9","9:16"],"supported_sizes":["1280x720","1080x1920","1920x1080","720x1280","3840x2160","2160x3840"],"supported_durations":[4,6,8],"supported_frame_images":["first_frame","last_frame"],"generate_audio":true,"seed":true,"pricing_skus":{"duration_seconds_with_audio":"0.40","duration_seconds_with_audio_4k":"0.60","duration_seconds_without_audio":"0.20","duration_seconds_without_audio_4k":"0.40"}},
{"id":"bytedance/seedance-2.0","name":"ByteDance: Seedance 2.0","created":1776211362,"supported_resolutions":["480p","720p","1080p","4K"],"supported_aspect_ratios":["1:1","3:4","9:16","4:3","16:9","21:9","9:21"],"supported_sizes":["854x480","1280x720","1920x1080"],"supported_durations":[4,5,6,7,8,9,10,11,12,13,14,15],"supported_frame_images":["first_frame","last_frame"],"generate_audio":true,"pricing_skus":{"video_tokens":"0.000007","video_tokens_4k":"0.000004","video_tokens_1080p":"0.0000077","video_tokens_without_audio":"0.000007","video_tokens_with_video_input":"0.0000043"}},
{"id":"x-ai/grok-imagine-video-1.5-lite","name":"SpaceXAI: Grok Imagine Video 1.5 Lite","created":1791309778,"supported_resolutions":["480p","720p","1080p"],"supported_aspect_ratios":["16:9","9:16","1:1"],"supported_sizes":null,"supported_durations":[1,2,3,4,5,6,7,8],"supported_frame_images":["first_frame"],"generate_audio":null,"pricing_skus":{"cents_per_image_input":"1","cents_per_video_output_second_480p":"2","cents_per_video_output_second_720p":"3","cents_per_video_output_second_1080p":"14"}},
{"id":"black-forest-labs/flux-video-edit","name":"Black Forest Labs: FLUX Video Edit","created":1789073532,"supported_durations":null,"generate_audio":false,"pricing_skus":{"cents_per_second_output":"3"}},
{"id":"vendor/chat","name":"Vendor: Chat","created":1,"context_length":1000,"pricing":{"prompt":"0.000001","completion":"0.000002"},"architecture":{"input_modalities":["text"],"output_modalities":["text"]}}
]}`

func clipCatalog(t *testing.T) map[string]zenModel {
	t.Helper()
	fees(t, map[string]string{"openrouter": "5.5", "hanzo": "5.5"})
	models, err := openrouterCatalog([]byte(videoListing))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]zenModel{}
	for _, m := range models {
		out[m.ID] = m
	}
	return out
}

// A video model is a SKU that answers a clip: it takes a prompt (and an image
// where it starts from one), every clip spends cash, and its card is its vendor's
// price SKUs times the markup. A model that makes no clip from a prompt is not
// listed.
func TestAVideoModelIsAClipSKU(t *testing.T) {
	ms := clipCatalog(t)
	if _, listed := ms["black-forest-labs/flux-video-edit"]; listed {
		t.Error("an editor that takes a video in is listed")
	}
	veo, ok := ms["google/veo-3.1"]
	if !ok || veo.Video == nil {
		t.Fatalf("veo is not a clip SKU: %+v", veo)
	}
	if strings.Join(veo.Outputs, ",") != "video" || strings.Join(veo.Inputs, ",") != "text,image" {
		t.Errorf("veo answers %v from %v, want video from text and image", veo.Outputs, veo.Inputs)
	}
	if !veo.Variable || !veo.priced() || veo.MinTier != "paid" || veo.Funding != "prepaid" {
		t.Errorf("veo variable=%v priced=%v tier=%q funding=%q, want a priced clip on both paid floors", veo.Variable, veo.priced(), veo.MinTier, veo.Funding)
	}
	// 0.40 × 1.055 × 1.055 = 0.44521
	if got := veo.Card.Rates["duration_seconds_with_audio"]; got != "0.44521" {
		t.Errorf("veo's card bills a second with audio at %s, want 0.44521", got)
	}
	if strings.Join(veo.Video.Resolutions, ",") != "720p,1080p,4k" {
		t.Errorf("veo resolutions %v", veo.Video.Resolutions)
	}
	if grok := ms["x-ai/grok-imagine-video-1.5-lite"]; strings.Join(grok.Inputs, ",") != "text,image" {
		t.Errorf("grok takes %v", grok.Inputs)
	}
	if chat := ms["vendor/chat"]; chat.Video != nil || chat.Variable {
		t.Errorf("a text model read as a clip: %+v", chat)
	}
}

// A clip is reserved at the dearest rate that can apply to what it asks for, for
// the seconds it asks for.
func TestAClipIsReservedAtTheMostItCanCost(t *testing.T) {
	ms := clipCatalog(t)
	yes, no := true, false
	for _, c := range []struct {
		model string
		ask   videoAsk
		want  string
	}{
		// 4 s at 720p with audio: 0.40 a second.
		{"google/veo-3.1", videoAsk{}, "1.6"},
		// 4K with audio: the 4K rate.
		{"google/veo-3.1", videoAsk{Resolution: "4k"}, "2.4"},
		// No audio: half.
		{"google/veo-3.1", videoAsk{Audio: &no, Seconds: 8}, "1.6"},
		// 720p 16:9 is 1280×720 at 24 frames, 21,600 tokens a second at $0.000007.
		{"bytedance/seedance-2.0", videoAsk{Audio: &yes}, "0.6048"},
		// 480p from an image: 2¢ a second and 1¢ the image.
		{"x-ai/grok-imagine-video-1.5-lite", videoAsk{Resolution: "480p", First: "https://x/i.png"}, "0.09"},
	} {
		m := ms[c.model]
		a := c.ask
		if err := m.Video.admit(&a); err != nil {
			t.Fatalf("%s %+v: %v", c.model, c.ask, err)
		}
		got, ok := m.Video.ceiling(a)
		if !ok || got.Cmp(decimal.MustParse(c.want)) != 0 {
			t.Errorf("%s %+v: ceiling %s %v, want %s", c.model, a, got, ok, c.want)
		}
	}
}

// What a model does not make is refused before anything is spent, naming what it
// does.
func TestAClipOutsideWhatTheModelMakesIsRefused(t *testing.T) {
	ms := clipCatalog(t)
	for _, c := range []struct {
		model string
		ask   videoAsk
		says  string
	}{
		{"google/veo-3.1", videoAsk{Seconds: 5}, "4, 6, 8"},
		{"google/veo-3.1", videoAsk{Resolution: "480p"}, "720p, 1080p, 4k"},
		{"google/veo-3.1", videoAsk{Aspect: "1:1"}, "16:9, 9:16"},
		{"x-ai/grok-imagine-video-1.5-lite", videoAsk{Last: "https://x/l.png"}, "end on an image"},
		{"black-forest-labs/flux-video-edit", videoAsk{}, ""},
		{"google/veo-3.1", videoAsk{Size: "640x640"}, "1280x720"},
		{"x-ai/grok-imagine-video-1.5-lite", videoAsk{Size: "1x"}, "WIDTHxHEIGHT"},
	} {
		m, ok := ms[c.model]
		if !ok {
			continue // not listed at all
		}
		a := c.ask
		err := m.Video.admit(&a)
		if err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s %+v: %v, want a refusal naming %q", c.model, c.ask, err, c.says)
		}
	}
}

// mp4Of is the smallest MP4 the reader reads: a file type box and a movie whose
// header states duration over timescale and whose one track is w×h.
func mp4Of(duration, timescale uint32, w, h int) []byte {
	box := func(kind string, body ...[]byte) []byte {
		n := 8
		for _, b := range body {
			n += len(b)
		}
		out := make([]byte, 8, n)
		binary.BigEndian.PutUint32(out, uint32(n))
		copy(out[4:], kind)
		for _, b := range body {
			out = append(out, b...)
		}
		return out
	}
	mvhd := make([]byte, 100)
	binary.BigEndian.PutUint32(mvhd[12:], timescale)
	binary.BigEndian.PutUint32(mvhd[16:], duration)
	tkhd := make([]byte, 84)
	binary.BigEndian.PutUint32(tkhd[76:], uint32(w)<<16)
	binary.BigEndian.PutUint32(tkhd[80:], uint32(h)<<16)
	return append(box("ftyp", []byte("isom\x00\x00\x02\x00isomiso2")), box("moov", box("mvhd", mvhd), box("trak", box("tkhd", tkhd)))...)
}

func TestAClipsFileSaysItsLengthAndFrame(t *testing.T) {
	b := mp4Of(8000, 1000, 1280, 720)
	info, err := readMP4(bytes.NewReader(b), int64(len(b)))
	if err != nil || info.seconds != 8 || info.width != 1280 || info.height != 720 {
		t.Fatalf("read %+v %v, want 8 s at 1280×720", info, err)
	}
	if _, err := readMP4(bytes.NewReader([]byte("not a movie at all")), 18); err == nil {
		t.Error("a file that is no MP4 read as one")
	}
}

// shelf is the host's object store in memory.
type shelf struct {
	mu   sync.Mutex
	kept map[string][]byte
}

func (s *shelf) Put(_ context.Context, key, _ string, r io.Reader, _ int64) error {
	b, err := io.ReadAll(r)
	s.mu.Lock()
	s.kept[key] = b
	s.mu.Unlock()
	return err
}

func (s *shelf) URL(_ context.Context, key string, ttl time.Duration) (string, time.Time, error) {
	return "https://s3.test/" + key, time.Now().Add(ttl), nil
}

// A clip from text: the vendor is asked for exactly the clip reserved, the job is
// filed, the watcher keeps the finished file in the host's store, reads its
// length and frame from it, and bills once what the vendor said it cost times the
// markup, never asked again.
func TestAClipIsMadeKeptReadAndBilledOnce(t *testing.T) {
	restore, err := object.UseMemoryDB("file:clip?mode=memory&cache=shared", &object.MediaJob{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	ms := clipCatalog(t)
	movie := mp4Of(4000, 1000, 1280, 720)

	var mu sync.Mutex
	var created []map[string]any
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/videos":
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			mu.Lock()
			created = append(created, b)
			mu.Unlock()
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"id":"job-1","status":"pending","polling_url":"x"}`)
		case r.URL.Path == "/v1/videos/job-1":
			mu.Lock()
			polls++
			n := polls
			mu.Unlock()
			if n < 2 {
				_, _ = io.WriteString(w, `{"id":"job-1","status":"in_progress"}`)
				return
			}
			_, _ = io.WriteString(w, `{"id":"job-1","status":"completed","unsigned_urls":["http://`+r.Host+`/v1/videos/job-1/content?index=0"],"usage":{"cost":1.2,"is_byok":false}}`)
		case r.URL.Path == "/v1/videos/job-1/content":
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(movie)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	fam := spareFamily(t, srv.URL, "vendor/free:free")
	veo := ms["google/veo-3.1"]
	fam.byID[veo.ID] = veo
	fam.ids = append(fam.ids, veo.ID)
	t.Setenv("OPENROUTER_API_KEY", "k1")
	t.Setenv("OPENROUTER_API_KEY_2", "")
	t.Setenv("OPENROUTER_API_KEY_3", "")
	forgetKeys()
	t.Cleanup(forgetKeys)
	kept := &shelf{kept: map[string][]byte{}}
	object.SetAssets(kept)
	t.Cleanup(func() { object.SetAssets(nil) })
	prev := clipPoll
	clipPoll = 10 * time.Millisecond
	t.Cleanup(func() { clipPoll = prev })
	got := ledger(t)

	body := `{"model":"google/veo-3.1","prompt":"a lighthouse at dusk","seconds":4,"resolution":"720p","aspect_ratio":"16:9"}`
	c := visit(http.MethodPost, "/v1/videos/generations")
	c.Fiber().Request().SetBody([]byte(body))
	alice := &iam.User{Owner: "acme", Name: "alice"}
	c.vendorClip(veo, []byte(body), alice)
	var answer clipStatus
	if err := json.Unmarshal([]byte(sent(c)), &answer); err != nil || answer.ID == "" || answer.Status != "queued" {
		t.Fatalf("create answered %s (%v)", sent(c), err)
	}
	// 4 s × 0.40 × 1.113025
	if answer.Cost == nil || answer.Cost.Reserved != "1.78084" {
		t.Errorf("reserved %+v, want 1.78084", answer.Cost)
	}
	mu.Lock()
	b := created[0]
	mu.Unlock()
	if b["model"] != "google/veo-3.1" || b["duration"] != float64(4) || b["resolution"] != "720p" || b["aspect_ratio"] != "16:9" || b["prompt"] != "a lighthouse at dusk" {
		t.Errorf("the vendor was asked %v", b)
	}

	var row *object.MediaJob
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if row, _ = object.MediaJobNamed(answer.ID); row != nil && row.Settled {
			break
		}
	}
	if row == nil || !row.Settled || row.Status != "completed" {
		t.Fatalf("the job stands %+v", row)
	}
	if row.Width != 1280 || row.Height != 720 || row.Duration != 4 || row.Bytes != int64(len(movie)) {
		t.Errorf("the kept file reads %d×%d %v s %d bytes", row.Width, row.Height, row.Duration, row.Bytes)
	}
	if !bytes.Equal(kept.kept[row.Asset], movie) || row.Asset != "videos/acme/"+answer.ID+".mp4" {
		t.Errorf("kept %q: %d bytes", row.Asset, len(kept.kept[row.Asset]))
	}
	// $1.20 × 1.113025 billed; $1.20 × 1.055 its cost of record.
	ev := got()
	if len(ev) != 1 || ev[0].USD != "1.33563" || ev[0].CostUSD != "1.266" || ev[0].Model != "google/veo-3.1" {
		t.Fatalf("debits %+v, want one of $1.33563 at a cost of $1.266", ev)
	}
	if row.Billed != "1.33563" || row.Cost != "1.2" {
		t.Errorf("the job says it billed %s at a cost of %s", row.Billed, row.Cost)
	}
	done := clipAnswer(context.Background(), *row)
	if done.URL != "https://s3.test/"+row.Asset || done.Width != 1280 || done.Duration != 4 || done.Progress != 100 {
		t.Errorf("the finished job answers %+v", done)
	}
	// Watched no more, and never billed again.
	if _, watched := clipJobs.Load(answer.ID); watched {
		t.Error("a finished job is still watched")
	}
	ResumeVideos()
	time.Sleep(50 * time.Millisecond)
	if len(got()) != 1 {
		t.Errorf("a finished job billed again: %+v", got())
	}
}

// A clip the vendor could not make bills nothing.
func TestAFailedClipBillsNothing(t *testing.T) {
	restore, err := object.UseMemoryDB("file:clipfail?mode=memory&cache=shared", &object.MediaJob{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	ms := clipCatalog(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"id":"job-2","status":"pending"}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"job-2","status":"failed","error":{"message":"the prompt was refused by the safety filter"}}`)
	}))
	t.Cleanup(srv.Close)
	fam := spareFamily(t, srv.URL, "vendor/free:free")
	veo := ms["google/veo-3.1"]
	fam.byID[veo.ID] = veo
	t.Setenv("OPENROUTER_API_KEY", "k1")
	forgetKeys()
	t.Cleanup(forgetKeys)
	object.SetAssets(&shelf{kept: map[string][]byte{}})
	t.Cleanup(func() { object.SetAssets(nil) })
	prev := clipPoll
	clipPoll = 10 * time.Millisecond
	t.Cleanup(func() { clipPoll = prev })
	got := ledger(t)

	c := visit(http.MethodPost, "/v1/videos/generations")
	c.vendorClip(veo, []byte(`{"model":"google/veo-3.1","prompt":"x"}`), &iam.User{Owner: "acme", Name: "bob"})
	var answer clipStatus
	_ = json.Unmarshal([]byte(sent(c)), &answer)
	var row *object.MediaJob
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if row, _ = object.MediaJobNamed(answer.ID); row != nil && row.Settled {
			break
		}
	}
	if row == nil || row.Status != "failed" || !strings.Contains(row.Error, "safety filter") {
		t.Fatalf("the job stands %+v", row)
	}
	if len(got()) != 0 {
		t.Errorf("a failed clip billed %+v", got())
	}
}
