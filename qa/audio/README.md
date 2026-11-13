# `qa/audio` — Audio Generation & Transcription QA (#133)

Reproducible test suite for the gateway's OpenAI-compatible speech surface:
`POST /v1/audio/speech` (Kokoro, pinned as `zen-voice-mini`) and
`POST /v1/audio/transcriptions` (+ `/transcriptions/public`, Parakeet/Whisper,
pinned as `zen-scribe`). Stdlib only — same shape as `qa/crossmodel` (#138)
and `qa/multimodal` (#135): offline checks run in CI without a network or a
credential, live lanes skip with their reason and never pass silently.

## Coverage map

| What #133 asks to test | Offline (CI, always) | Live (opt-in) |
|---|---|---|
| TTS across formats `mp3 wav opus aac flac pcm` | every container is built byte-for-byte and walked structurally (`mkMP3/mkOGGOpus/mkFLAC/mkaacFrames`, `TestSniffAndValidate`); corrupt variants must fail with the *right* error (`TestCorruptContainersFail`) | full `response_format` matrix against the real gateway: valid container, honest label, audible signal, exact format back (`TestLiveSpeechFormats`) |
| Voices, speeds | request shape pins `voice`/`speed` and their omission rules (`TestSpeechBodyWireShape`) | named voices must not 5xx; `speed=0.8` vs `1.3` must move the duration (`TestLiveSpeechVoicesSpeeds`) |
| Streaming audio chunks | streamed reads reassemble to byte-identical, still-valid containers (`TestSpeechStreamedReassembles`) | 4 KiB chunk consumption, Content-Length agreement, container validity (`TestLiveSpeechStreaming`) |
| STT multilingual, accents, technical terms | the phrase corpus is self-consistent by construction — every sentence contains its own pinned keywords (`TestPhrasesCorpusIsSelfConsistent`: en, en-tech, ru, uk, es, fr, de, zh, ja) | synthesize each phrase, transcribe it back, demand real words: English rows must reproduce ≥ half their keywords, every language ≥ one (`TestLiveTranscribeRoundTrip`) |
| Word/segment timestamps (`verbose_json`, `timestamp_granularities`) | the checker is proven against canned conforming **and** nine divergent bodies — overlaps, escaping words, nulls, drift, disagreement between text/segments/words (`TestCheckTimingsCatchDivergence`); the form must carry the bracketed array spelling (`TestTranscribeFormWireShape`) | round trip and long lanes request `word` **and** `segment` and audit every timestamp (`stt-timings` findings) |
| Long recordings, chunk boundaries | spliced fixtures sum exactly (`TestConcatWAVSumsDurations`); a 2.4 s transcript over 60 s of audio is flagged as truncation (`TestCheckCoverageCatchesTruncation`) | ~730 chars → ~minute of audio, transcribed end to end with coverage demanded at both ends (`TestLiveLongRecording`) |
| Silence / interruption handling | generated digital silence (`Silence`), speech-shaped onsets at known seconds (`SpeechLike`) | silence must not become a hallucinated paragraph; the public door answers anonymous uploads cleanly (`TestLiveSilenceHandling`, `TestLivePublicTranscriptions`) |
| Loudness / output consistency | RMS/peak over decoded PCM: a half-scale sine reads 0.354, silence reads 0 (`TestLevel`); "valid container full of silence" fails the format lane, not CI's eye | per-format loudness floor on synthesis responses (`tts-loudness` findings) |
| Mislabelled containers (the `audio/opus`-carrying-MP3 class from `audio_api.go` history) | bytes are sniffed without consulting the label; the exact historical mislabel is refused (`TestMislabelCaught`) | every 200 response: Content-Type must name what the bytes actually are (`tts-formats-label`) |

## Files

| File | Role |
|---|---|
| `audio.go` | env/lane constants, pinned SKUs, gateway root resolution (shared env names with #138/#135) |
| `formats.go` | container sniffing + structural validators for all six formats: MP3 frame chain + Xing count vs truncation, FLAC STREAMINFO walk, Ogg page CRC-32 recomputation + pre-skip duration math, ADTS walk, WAVE chunk chain, s16le loudness |
| `fixtures.go` | every WAV fixture generated at run time — sine, silence, speech-shaped envelope, deterministic noise, spliced long recordings; the nine-language phrase corpus with pinned keywords |
| `transcript.go` | `verbose_json` decode + the timestamp audit: monotonicity, word-in-segment containment, duration-vs-container drift, text/segments/words agreement, coverage at both ends |
| `client.go` | minimal OpenAI speech/transcribe/public client + chunked stream consumer |
| `report.go` | live findings → `HANZO_QA_REPORT` JSON artifact + Markdown twin (the request/response trace the acceptance criteria ask for) |
| `*_test.go` | offline proofs above, then the gated live lanes |

## Run it

```bash
# CI: no network, no credential — the whole structural suite
go test ./qa/audio -count=1

# key-free public lane (the one that runs against api.hanzo.ai today)
HANZO_QA_LIVE=1 go test ./qa/audio -run TestLivePublicTranscriptions -v

# full keyed matrix — format matrix, round trips, long recording, streaming
HANZO_QA_LIVE=1 HANZO_API_KEY=sk-... \
  HANZO_QA_REPORT=/tmp/audio-report.json go test ./qa/audio -run TestLive -v
# writes /tmp/audio-report.json + /tmp/audio-report.md (findings table)
```

`HANZO_API_URL` retargets the gateway; the two pinned SKUs (`zen-voice-mini`,
`zen-scribe`) are checked against `/v1/models` first, so a catalog move fails
loudly instead of silently testing nothing.

## Findings on `api.hanzo.ai` today (key-free run, 2026-11-10)

`HANZO_QA_LIVE=1 go test ./qa/audio -run TestLivePublicTranscriptions` reproduces:

| Check | Today | Expected |
|---|---|---|
| anonymous 3 s tone → `/v1/audio/transcriptions/public` | **200, decodable transcript** — the door works without a credential | — |
| anonymous 8 bytes of garbage → same door | **HTTP 502**, body `error code: 502` (plain text) | HTTP 400 + OpenAI's JSON error envelope, *before* anything is forwarded |

Undecodable audio reaches the upstream instead of being refused at the edge:
a transcriber decode failure today costs an upstream round trip and surfaces
as a 5xx with a non-JSON body, which makes anonymous uploads a capacity lever
and breaks every SDK's error parsing (the envelope half is #138's standing
finding — the status-code half belongs here). Where to fix it: after
`parseTranscribeForm` in `controllers/audio_api.go` / `zap_audio.go`, sniff the
file part with the same container rules as this suite (`formats.go`) and answer
400 `invalid_file_format` when nothing decodes, instead of forwarding bytes the
speech service will choke on.

## Not covered here

The keyed lanes (TTS format matrix, voice/speed probes, streaming, nine-language
round trips, long recording, silence) need `HANZO_API_KEY`; without one they
skip with their reason — the PR that claims them should paste its
`report.md` from a keyed run. Compressed-container loudness is structural only:
mp3/opus/aac arrive valid but are not PCM-decoded with stdlib alone
(`SignalLevel` reports `ok=false` rather than faking a level) — add a pure-Go
decoder if "silence inside a valid MP3" ever shows up in production.
