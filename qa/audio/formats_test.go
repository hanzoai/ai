// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package audio

import (
	"encoding/binary"
	"math"
	"strings"
	"testing"
)

// The container builders below mint minimal valid files for every format in
// the matrix — no binary blobs, and each corruption test reuses one with a
// single field twisted, which is how a validator gets proven to actually read
// structure rather than just magic bytes.
// shortcut: hand-built containers exercise the header math, not real encoder
// quirks; if production containers grow exotic (ID3v2.4 footers, multi-stream
// Ogg), pin captured samples as golden fixtures at that point.

// mkMP3 builds frames MPEG-1 Layer III frames at 128 kbps / 44.1 kHz stereo,
// first frame carrying a Xing header that promises `declared` total frames —
// declared != frames is the truncated-output case.
func mkMP3(frames int, declared int) []byte {
	const frameLen = 144000 * 128 / 44100 // 417, no padding
	out := make([]byte, 0, frames*frameLen)
	for f := 0; f < frames; f++ {
		fr := make([]byte, frameLen)
		fr[0], fr[1], fr[2], fr[3] = 0xFF, 0xFB, 0x90, 0x00 // mpeg1 L3, 128k, 44.1k, stereo, no CRC
		for i := 4; i < frameLen; i++ {
			fr[i] = byte(i%251 + 1) // never 0x00/0xFF: truncation must not masquerade as padding
		}
		if f == 0 && declared > 0 {
			copy(fr[36:], "Xing")                  // MPEG-1 Layer III stereo side info ends at 36
			binary.BigEndian.PutUint32(fr[40:], 1) // frames field present
			binary.BigEndian.PutUint32(fr[44:], uint32(declared))
		}
		out = append(out, fr...)
	}
	return out
}

// mkFLAC builds the shortest legal FLAC stream: fLaC magic plus one last-flag
// STREAMINFO block stating rate × total samples.
func mkFLAC(rate int, total uint64) []byte {
	body := make([]byte, 34)
	v := uint64(rate)<<44 | 0<<41 | 15<<36 | total // mono, 16-bit
	binary.BigEndian.PutUint64(body[10:], v)
	out := make([]byte, 0, 4+4+len(body))
	out = append(out, 'f', 'L', 'a', 'C')
	out = append(out, 0x80, 0x00, 0x00, byte(len(body))) // last flag, type STREAMINFO, len 34
	return append(out, body...)
}

// mkOGGPage wraps one ≤255-byte payload as an Ogg page: 27-byte header, a
// one-entry segment table, then the payload — with a correctly computed CRC.
func mkOGGPage(hdrType byte, granule int64, serial, seq uint32, payload []byte) []byte {
	p := make([]byte, 28+len(payload)) // 27-byte header + one-entry segment table + payload
	copy(p[0:], "OggS")
	p[4] = 0 // version
	p[5] = hdrType
	binary.LittleEndian.PutUint64(p[6:], uint64(granule))
	binary.LittleEndian.PutUint32(p[14:], serial)
	binary.LittleEndian.PutUint32(p[18:], seq)
	p[26] = 1                  // one segment entry…
	p[27] = byte(len(payload)) // …saying the packet ends here (< 255)
	copy(p[28:], payload)
	binary.LittleEndian.PutUint32(p[22:], oggCRC32(crcZeroedPage(p)))
	return p
}

// opusIdentity is the OpusHead identity packet: version 1, mono, pre-skip 312.
func opusIdentity() []byte {
	head := make([]byte, 19)
	copy(head, "OpusHead")
	head[8], head[9] = 1, 1
	binary.LittleEndian.PutUint16(head[10:], 312)
	binary.LittleEndian.PutUint32(head[12:], 48000)
	return head
}

const opusPreSkip = 312

// mkOGGOpus builds a two-page Opus stream: bos page with the identity packet,
// eos page ending at `dur` seconds.
func mkOGGOpus(dur float64) []byte {
	out := mkOGGPage(0x02, -1, 0x5EED, 0, opusIdentity())
	granule := int64(opusPreSkip + dur*48000)
	return append(out, mkOGGPage(0x04, granule, 0x5EED, 1, []byte{0x58, 0x00, 0x00})...)
}

// mkaacFrames builds frames ADTS AAC-LC frames at 48 kHz.
func mkaacFrames(frames int) []byte {
	const frameLen = 7 + 160
	out := make([]byte, 0, frames*frameLen)
	for f := 0; f < frames; f++ {
		fr := make([]byte, frameLen)
		fr[0], fr[1] = 0xFF, 0xF1 // sync, layer 00, protection absent
		fr[2] = 1<<6 | 3<<2       // AAC-LC profile, frequency index 3 (48 kHz)
		fr[3] = byte(2<<6) | byte(frameLen>>11)
		fr[4] = byte(frameLen >> 3)
		fr[5] = byte(frameLen&7)<<5 | 0x1F
		fr[6] = 0xFC
		for i := 7; i < frameLen; i++ {
			fr[i] = byte(i%251 + 1)
		}
		out = append(out, fr...)
	}
	return out
}

func TestSniffAndValidate(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want Format
		dur  float64 // expected duration; <0 → not asserted
	}{
		{"mp3", mkMP3(50, 50), MP3, 50 * 1152 / 44100.0},
		{"wav", Sine(440, 1.5), WAV, 1.5},
		{"opus", mkOGGOpus(2.0), Opus, 2.0},
		{"aac", mkaacFrames(10), AAC, 10 * 1024 / 48000.0},
		{"flac", mkFLAC(24000, 24000*3), FLAC, 3.0},
		{"pcm", []byte{0, 0, 1, 0, 2, 0}, PCM, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.want != PCM { // headerless PCM has no magic; Sniff stays "", Validate finds the format
				if got := Sniff(tc.data); got != tc.want {
					t.Fatalf("Sniff = %q, want %q", got, tc.want)
				}
			}
			info, err := Validate(tc.data)
			if err != nil {
				t.Fatalf("Validate valid %s: %v", tc.name, err)
			}
			if info.Format != tc.want {
				t.Errorf("info format = %q, want %q", info.Format, tc.want)
			}
			if tc.dur >= 0 && math.Abs(info.DurationSeconds-tc.dur) > 0.02 {
				t.Errorf("duration = %.4f s, want %.4f s", info.DurationSeconds, tc.dur)
			}
		})
	}
}

func TestCorruptContainersFail(t *testing.T) {
	mp3Full := mkMP3(50, 50)
	wavLie := Sine(440, 1.0)
	binary.LittleEndian.PutUint32(wavLie[40:], 1<<20) // data chunk promises a megabyte

	cases := []struct {
		name    string
		data    []byte
		wantErr string // substring of the structural error; "" → any error
	}{
		{"mp3-truncated", mp3Full[:len(mp3Full)*6/10], "Xing header promises"},
		{"mp3-cut-mid-frame", cutMidMP3Frame(mp3Full), "unpadded bytes after frame"},
		{"not-audio-odd", []byte{1, 2, 3, 4, 5}, "not s16le PCM either"},
		{"wav-lies-about-size", wavLie, "chunk declares"},
		{"wav-truncated-here", Sine(440, 1.0)[:400], "RIFF size declares"},
		{"opus-bad-crc", flipByte(mkOGGOpus(2.0), 40), "CRC is"},
		{"opus-no-eos", mkOGGPage(0x02, -1, 7, 0, opusIdentity()), "end-of-stream"},
		{"opus-seq-gap", gapSeqOpus(), "sequence number"},
		{"flac-mid-header", mkFLAC(24000, 72000)[:16], "remain"},
		{"flac-no-streaminfo", noStreamInfoFLAC(), "STREAMINFO"},
		{"aac-truncated", mkaacFrames(5)[:30], "truncated output"},
		{"aac-bad-freq", badFreqAAC(), "frequency index"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Validate(tc.data)
			if err == nil || (tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("error = %v, want one containing %q", err, tc.wantErr)
			}
		})
	}
}

// cutMidMP3Frame cuts one frame short mid-payload with no Xing to appeal to.
func cutMidMP3Frame(full []byte) []byte {
	noXing := mkMP3(50, 0)
	return noXing[:20*417+200] // 20 whole frames + a partial payload tail
}

func flipByte(b []byte, at int) []byte {
	out := append([]byte(nil), b...)
	out[at] ^= 0xFF
	return out
}

// gapSeqOpus is a valid-CRC Opus stream whose second page jumps the sequence
// number — a page was dropped somewhere in transit.
func gapSeqOpus() []byte {
	out := mkOGGPage(0x02, -1, 0x5EED, 0, opusIdentity())
	eos := mkOGGPage(0x04, int64(opusPreSkip+48000), 0x5EED, 7, []byte{0x58, 0x00, 0x00})
	return append(out, eos...)
}

func noStreamInfoFLAC() []byte {
	return []byte{'f', 'L', 'a', 'C', 0x81, 0x00, 0x00, 0x04, 1, 2, 3, 4} // last-flag PADDING block first
}

func badFreqAAC() []byte {
	fr := mkaacFrames(1)
	fr[2] = 1<<6 | 15<<2 // frequency index 15 is reserved
	return fr
}

// TestMislabelCaught pins the historical bug class from audio_api.go: a
// response labelled audio/opus whose bytes are MP3. The live lane runs exactly
// this comparison; these assertions prove the comparison itself works.
func TestMislabelCaught(t *testing.T) {
	mp3 := mkMP3(20, 20)
	if got := Sniff(mp3); got != MP3 {
		t.Fatalf("mp3 sniffed as %q", got)
	}
	if LabelMatchesFormat("audio/opus", MP3) {
		t.Error(`"Content-Type: audio/opus" must not be accepted for an MP3 body`)
	}
	if !LabelMatchesFormat("audio/mpeg", MP3) || !LabelMatchesFormat("audio/x-wav", WAV) {
		t.Error("correct labels must be accepted")
	}
}

func TestLevel(t *testing.T) {
	cases := []struct {
		name    string
		data    []byte
		wantRMS float64
		tol     float64
	}{
		{"sine-half-scale", Sine(440, 1), 0.5 / math.Sqrt2, 0.01},
		{"silence", Silence(1), 0, 1e-9},
		{"noise-band", Noise(1), 0.14, 0.06},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pcm, err := wavPCM(tc.data)
			if err != nil {
				t.Fatal(err)
			}
			rms, peak := Level(pcm)
			if math.Abs(rms-tc.wantRMS) > tc.tol {
				t.Errorf("rms = %.4f, want %.4f ±%.4f", rms, tc.wantRMS, tc.tol)
			}
			if tc.wantRMS > 0 && peak == 0 {
				t.Error("nonzero rms with zero peak — impossible")
			}
		})
	}
}
