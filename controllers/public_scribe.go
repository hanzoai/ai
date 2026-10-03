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

// public_scribe.go — a transcription for someone with no account.
//
// The mic on our own pages is offered to visitors who have not signed in, and it
// is held by the rules public_chat.go holds a completion to, for the same
// reasons:
//
//   - THE CEILING IS THE SWITCH. publicScribeDaily is transcriptions per visitor
//     per UTC day, and zero — the default — means the lane does not exist.
//   - IT FAILS CLOSED, AND ALONE. The count is kept in this process and asks no
//     other service, so it holds while anything else is down.
//   - A DAY IS SPENT ON ANSWERS. The count is read on arrival and raised only
//     where a transcript comes back.
//   - THE VISITOR IS THE ADDRESS THE EDGE OBSERVED (Lanes): an IPv6 visitor is held
//     by their /64 and by their /48 together, the /48 at siteDay times the limit.
//
// And two rules of its own. The MODEL IS ASSIGNED: publicScribeModel, our own
// speech service, which pays no vendor — a route that would spend on a vendor
// closes the lane rather than serving, so nothing a stranger sends reaches the paid
// lane. And the AUDIO IS HELD TO publicScribeSeconds: the speech service stops
// decoding just past it and refuses what runs over, so the cap bounds the work. The
// audio is relayed and dropped; nothing here keeps it or logs what it said.
package controllers

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/stt"
)

// publicScribeModel is the transcriber a visitor is served by. Not a choice the
// request makes: whatever model it names is ignored.
const publicScribeModel = "zen-scribe"

// publicScribeSeconds is the most audio one public transcription takes.
const publicScribeSeconds = 60

// publicScribeUpload bounds the body of one public transcription. A minute of
// what a browser records (opus or aac, 24-128 kb/s) is well under it; the length
// itself is held by publicScribeSeconds, since bytes do not say how long audio is.
const publicScribeUpload = 2 << 20

// publicScribeDaily is the ceiling and the switch at once: transcriptions one
// visitor may take per UTC day. 0 closes the lane.
func publicScribeDaily() int { return conf.GetConfigInt("PUBLIC_SCRIBE_DAILY") }

// publicScribes is the lane's own day, apart from the chat lane's: a visitor who
// dictated has not spent a free message.
var publicScribes = &dayCount{}

// AudioTranscriptionsPublic transcribes up to a minute of audio for a caller with no
// account, on Hanzo's own transcriber, within a daily allowance per visitor.
//
// @Title AudioTranscriptionsPublic
// @Tag Audio API
// @Description Anonymous speech-to-text on Hanzo's own transcriber. No credential is
// presented and none is issued; the model is assigned and the audio is held to 60 s.
// @Param file formData file true "the audio to transcribe, at most 60 s"
// @Param language formData string false "the spoken language, if known"
// @Success 200 {object} controllers.transcriptionResponse "transcription"
// @router /audio/transcriptions/public [post]
func (c *ApiController) AudioTranscriptionsPublic() {
	daily := publicScribeDaily()
	if daily <= 0 {
		c.publicRefuse(http.StatusNotFound, "invalid_request_error", "public_lane_closed",
			"This deployment does not serve anonymous transcription.")
		return
	}
	lanes := Lanes(c.Ctx)
	if len(lanes) == 0 {
		c.publicRefuse(http.StatusForbidden, "invalid_request_error", "public_no_address",
			"This request carries no address to count against.")
		return
	}
	body := c.Body()
	if len(body) > publicScribeUpload {
		c.publicRefuse(http.StatusRequestEntityTooLarge, "invalid_request_error", "public_audio_too_long",
			fmt.Sprintf("Anonymous transcription takes at most %d seconds of audio.", publicScribeSeconds))
		return
	}
	form, err := parseTranscribeForm(body)
	if err != nil || form.audio == nil {
		c.publicRefuse(http.StatusBadRequest, "invalid_request_error", "public_no_audio",
			"Send the audio as the multipart field \"file\".")
		return
	}

	day := utcDay(time.Now())
	if publicScribes.out(lanes, day, daily) {
		log.Info("public_scribe: allowance spent visitor=%s", lanes[0].Key)
		c.publicRefuse(http.StatusTooManyRequests, "insufficient_quota", "public_allowance_spent",
			"You've used today's free dictation. It resets at midnight UTC — sign in at https://hanzo.ai to keep going.")
		return
	}

	provider, upstream, err := publicScribeProvider()
	if err != nil {
		c.publicRefuse(http.StatusServiceUnavailable, "server_error", "public_lane_unavailable",
			"Anonymous transcription is not available right now. Nothing was counted against you.")
		return
	}
	provider.SubType = upstream
	if form.language != "" {
		provider.Flavor = form.language
	}
	ear, err := provider.GetSpeechToTextProvider(c.GetAcceptLanguage())
	if err != nil {
		c.publicRefuse(http.StatusServiceUnavailable, "server_error", "public_lane_unavailable",
			"Anonymous transcription is not available right now. Nothing was counted against you.")
		return
	}

	release, refused := admitSpeech(publicOrg)
	if refused != nil {
		c.publicRefuse(statusOf(refused), "server_error", "public_lane_busy", refused.Error())
		return
	}
	defer release()

	heard, _, err := ear.ProcessAudio(form.audio, stt.WithLongest(c.Context(), publicScribeSeconds),
		c.GetAcceptLanguage(), nil)
	if errors.Is(err, stt.ErrTooLong) {
		c.publicRefuse(http.StatusRequestEntityTooLarge, "invalid_request_error", "public_audio_too_long",
			fmt.Sprintf("Anonymous transcription takes at most %d seconds of audio.", publicScribeSeconds))
		return
	}
	if err != nil {
		log.Warning("public_scribe: transcription failed visitor=%s: %v", lanes[0].Key, err)
		c.publicRefuse(http.StatusBadGateway, "server_error", "public_transcription_failed",
			"The audio could not be transcribed. Nothing was counted against you.")
		return
	}
	publicScribes.serve(lanes, day, daily)

	c.SetHeader("Cache-Control", "no-store")
	if form.responseFormat == "text" {
		c.SetHeader("Content-Type", "text/plain; charset=utf-8")
		c.Bytes(http.StatusOK, []byte(heard.Text))
		return
	}
	c.SetHeader("Content-Type", "application/json")
	c.Bytes(http.StatusOK, transcriptionBody("json", heard, 0))
}

// publicScribeProvider resolves the lane's one route, and refuses any route that
// would spend on a vendor: the provider must be a service we operate (operated),
// so a catalog edit that moved zen-scribe onto a paid upstream closes the lane
// instead of handing strangers that upstream.
func publicScribeProvider() (*object.Provider, string, error) {
	route := resolveModelRoute(publicScribeModel)
	if route == nil {
		return nil, "", fmt.Errorf("%s has no route", publicScribeModel)
	}
	provider, err := object.GetModelProviderByName(route.providerName)
	if err != nil || provider == nil {
		return nil, "", fmt.Errorf("%s's provider is unavailable", publicScribeModel)
	}
	if provider.Owner != "admin" || !operated[provider.Name] {
		return nil, "", fmt.Errorf("%s is served by %q, which is not ours", publicScribeModel, provider.Name)
	}
	return provider, route.upstreamModel, nil
}
