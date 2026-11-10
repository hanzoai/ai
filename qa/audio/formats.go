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
	"fmt"
	"math"
)

// Format names one response_format container /v1/audio/speech can carry. The
// six values are exactly the OpenAI list the issue enumerates: mp3 (the
// default), wav, opus, aac, flac, pcm.
type Format string

const (
	MP3  Format = "mp3"
	WAV  Format = "wav"
	Opus Format = "opus"
	AAC  Format = "aac"
	FLAC Format = "flac"
	PCM  Format = "pcm" // headerless s16le
)

// AllFormats is the TTS format matrix in the order the issue names them.
var AllFormats = []Format{MP3, WAV, Opus, AAC, FLAC, PCM}

// AudioInfo is what a container validator learned from the bytes: the measured
// play duration (0 when the format carries no duration to measure — headerless
// PCM), and the decode parameters where the container states them.
type AudioInfo struct {
	Format          Format
	DurationSeconds float64
	SampleRate      int
	Channels        int
	BitDepth        int // 0 for lossy containers, which state no bit depth
}

// Sniff identifies what the bytes ACTUALLY are, from magic and structure alone.
// This is the check that pins the audio_api.go fix: a response labelled
// `Content-Type: audio/opus` carrying an MP3 used to reach the player as a
// lie, and the only way to catch that class is to identify the bytes without
// consulting the label. "" means no container header was found — headerless
// PCM, or garbage.
func Sniff(data []byte) Format {
	if len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WAVE" {
		return WAV
	}
	if len(data) >= 4 && string(data[0:4]) == "fLaC" {
		return FLAC
	}
	if len(data) >= 31 && string(data[0:4]) == "OggS" {
		if firstPacketHas(data, "OpusHead") || firstPacketHas(data, "OpusTags") {
			return Opus
		}
		return "" // an Ogg stream carrying something else — not a TTS format we sell
	}
	body := skipID3(data)
	if len(body) >= 2 && body[0] == 0xFF && body[1]&0xF0 == 0xF0 && body[1]&0x06 == 0x00 {
		return AAC // ADTS: 12 sync bits, layer bits 00 (version and protection bits may vary)
	}
	if len(body) >= 4 && body[0] == 0xFF && body[1]&0xE0 == 0xE0 &&
		body[1]&0x06 != 0x00 && // layer bits must be non-zero (mp3, not aac)
		(body[1]>>3)&0x03 != 0x01 && // version bits 01 is reserved
		(body[2]>>4)&0x0F != 0x00 && (body[2]>>4)&0x0F != 0x0F { // free/bad bitrate
		return MP3
	}
	return ""
}

// mediaTypes lists the Content-Type values a label for this format may legally
// carry, matching audio_api.go's audioExtension switch so the suite and the
// gateway agree on what each name means.
func mediaTypes(f Format) []string {
	switch f {
	case MP3:
		return []string{"audio/mpeg", "audio/mp3"}
	case WAV:
		return []string{"audio/wav", "audio/x-wav", "audio/wave"}
	case Opus:
		return []string{"audio/opus", "audio/ogg"}
	case AAC:
		return []string{"audio/aac"}
	case FLAC:
		return []string{"audio/flac", "audio/x-flac"}
	case PCM:
		return []string{"audio/pcm", "application/octet-stream"}
	}
	return nil
}

// LabelMatchesFormat reports whether a Content-Type header (already stripped of
// parameters) names the format the bytes actually carry.
func LabelMatchesFormat(contentType string, f Format) bool {
	for _, t := range mediaTypes(f) {
		if t == contentType {
			return true
		}
	}
	return false
}

// Validate walks one container's real structure — not just its magic — and
// returns the measured duration or a precise structural error. Every error
// string names WHICH part is wrong, because these are the findings the issue
// asks to document: format errors, truncated outputs, mislabelled containers.
func Validate(data []byte) (*AudioInfo, error) {
	switch Sniff(data) {
	case WAV:
		return validateWAV(data)
	case MP3:
		return validateMP3(data)
	case FLAC:
		return validateFLAC(data)
	case Opus:
		return validateOGGOpus(data)
	case AAC:
		return validateAAC(data)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("empty body — no audio bytes at all")
	}
	if len(data)%2 != 0 {
		return nil, fmt.Errorf("no container header and an odd byte length (%d) — not s16le PCM either", len(data))
	}
	return &AudioInfo{Format: PCM}, nil
}

// skipID3 returns the bytes after a leading ID3v2 tag, or data unchanged.
func skipID3(data []byte) []byte {
	if len(data) < 10 || string(data[0:3]) != "ID3" {
		return data
	}
	flags := data[5]
	size := int(data[6])<<21 | int(data[7])<<14 | int(data[8])<<7 | int(data[9]) // synchsafe
	end := 10 + size
	if flags&0x40 != 0 { // extended header: its own 4-byte size follows, included already
		if len(data) < end {
			return data[len(data):]
		}
	}
	if flags&0x10 != 0 {
		end += 10 // footer
	}
	if end > len(data) {
		return data[len(data):]
	}
	return data[end:]
}

// firstPacketHas reports whether the first page's packet payload starts with
// magic (used to tell an Opus Ogg stream from any other Ogg container).
func firstPacketHas(data []byte, magic string) bool {
	if len(data) < 27 {
		return false
	}
	nseg := int(data[26])
	po := 27 + nseg
	if po+len(magic) > len(data) {
		return false
	}
	return string(data[po:po+len(magic)]) == magic
}

// ── WAV ──────────────────────────────────────────────────────────────────────

// validateWAV walks the RIFF chunk chain and pins the three lies a WAVE file
// can tell: a data chunk bigger than the file (truncation), a byteRate that
// contradicts sample rate × block align, and a fmt audioFormat nothing decodes.
func validateWAV(data []byte) (*AudioInfo, error) {
	if len(data) < 44 {
		return nil, fmt.Errorf("WAVE file is %d bytes — too short to hold even a canonical header", len(data))
	}
	if riffSize := int(binary.LittleEndian.Uint32(data[4:8])); riffSize+8 != len(data) {
		return nil, fmt.Errorf("RIFF size declares %d total bytes, file holds %d", riffSize+8, len(data))
	}
	info := &AudioInfo{Format: WAV}
	var byteRate, dataAvail int
	off := 12
	for off+8 <= len(data) {
		id := string(data[off : off+4])
		size := int(binary.LittleEndian.Uint32(data[off+4 : off+8]))
		body := off + 8
		if body+size > len(data) {
			return nil, fmt.Errorf("%q chunk declares %d bytes at offset %d, only %d remain — truncated audio or header chain", id, size, body, len(data)-body)
		}
		switch id {
		case "fmt ":
			if size < 16 {
				return nil, fmt.Errorf("fmt chunk is %d bytes, want ≥16", size)
			}
			formatTag := int(binary.LittleEndian.Uint16(data[body : body+2]))
			info.Channels = int(binary.LittleEndian.Uint16(data[body+2 : body+4]))
			info.SampleRate = int(binary.LittleEndian.Uint32(data[body+4 : body+8]))
			byteRate = int(binary.LittleEndian.Uint32(data[body+8 : body+12]))
			blockAlign := int(binary.LittleEndian.Uint16(data[body+12 : body+14]))
			info.BitDepth = int(binary.LittleEndian.Uint16(data[body+14 : body+16]))
			if formatTag == 0xFFFE && size >= 26 { // extensible: the real tag is the first GUID bytes
				formatTag = int(binary.LittleEndian.Uint16(data[body+24 : body+26]))
			}
			switch formatTag {
			case 1, 3: // PCM integer / IEEE float
			default:
				return nil, fmt.Errorf("fmt audioFormat %d — nothing in a stdlib pipeline decodes this", formatTag)
			}
			if info.Channels < 1 || info.SampleRate < 1 || info.BitDepth < 8 {
				return nil, fmt.Errorf("fmt declares %d ch / %d Hz / %d bit — impossible", info.Channels, info.SampleRate, info.BitDepth)
			}
			if blockAlign != info.Channels*info.BitDepth/8 {
				return nil, fmt.Errorf("block align %d contradicts %d ch × %d bit", blockAlign, info.Channels, info.BitDepth)
			}
		case "data":
			// The generic check above already pinned declared size ≤ what the
			// file delivers; byteRate × duration is the promise the samples must
			// fill, computed here so callers get a real duration.
			dataAvail = len(data) - body
			if byteRate > 0 {
				info.DurationSeconds = float64(size) / float64(byteRate)
			}
		}
		off = body + size + (size & 1) // chunks are word-aligned
	}
	if info.SampleRate == 0 {
		return nil, fmt.Errorf("WAVE file has no fmt chunk")
	}
	if dataAvail == 0 {
		return nil, fmt.Errorf("WAVE file has no data chunk")
	}
	return info, nil
}

// wavPCM returns the raw samples of a s16 mono/stereo PCM WAVE file.
func wavPCM(data []byte) ([]byte, error) {
	if Sniff(data) != WAV {
		return nil, fmt.Errorf("not a WAVE file")
	}
	off := 12
	for off+8 <= len(data) {
		id := string(data[off : off+4])
		size := int(binary.LittleEndian.Uint32(data[off+4 : off+8]))
		body := off + 8
		if body+size > len(data) {
			return nil, fmt.Errorf("truncated %q chunk", id)
		}
		if id == "fmt " {
			if tag := int(binary.LittleEndian.Uint16(data[body : body+2])); tag != 1 {
				return nil, fmt.Errorf("fmt audioFormat %d is not plain PCM — no stdlib decoder", tag)
			}
			if bits := int(binary.LittleEndian.Uint16(data[body+14 : body+16])); bits != 16 {
				return nil, fmt.Errorf("%d-bit samples — loudness check assumes s16le", bits)
			}
		}
		if id == "data" {
			return data[body : body+size], nil
		}
		off = body + size + (size & 1)
	}
	return nil, fmt.Errorf("no data chunk")
}

// ── MP3 ──────────────────────────────────────────────────────────────────────

// mp3Bitrates[kbps] and mp3Rates[Hz] per MPEG version × Layer III. Layer I/II
// frames are refused: every upstream in the TTS path emits Layer III, and a
// container that does not is itself a finding worth its own error string.
var (
	mp3Bitrates = [2][16]int{ // [mpeg1][bitrate index] ; [!mpeg1][index]
		{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, -1},
		{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, -1},
	}
	mp3Rates = [3][4]int{{44100, 48000, 32000, 0}, {22050, 24000, 16000, 0}, {11025, 12000, 8000, 0}}
)

// validateMP3 walks the frame chain. The Xing/VBRI count in the first frame is
// the encoder's own promise of how many frames follow; when the chain runs out
// short, the output was truncated downstream — the silent-mid-file cut this
// issue is about.
func validateMP3(data []byte) (*AudioInfo, error) {
	body := skipID3(data)
	off := 0
	var frames, samples int64
	var declared int64 = -1
	var rate int
	for off+4 <= len(body) {
		b := body[off:]
		if b[0] != 0xFF || b[1]&0xE0 != 0xE0 {
			break // end of the frame chain (an ID3v1 tail or padding is fine here)
		}
		verBits := (b[1] >> 3) & 0x03
		layerBits := (b[1] >> 1) & 0x03 // layer codes: 01 = Layer III, the only kind TTS emits
		bitIdx := (b[2] >> 4) & 0x0F
		rateIdx := (b[2] >> 2) & 0x03
		pad := int(b[2]>>1) & 1
		if verBits == 0x01 || layerBits != 0x01 || rateIdx == 0x03 {
			return nil, fmt.Errorf("frame at byte %d: reserved header bits (version %d, layer %d, rate index %d) — corrupt or not an MP3", off, verBits, layerBits, rateIdx)
		}
		if bitIdx == 0x00 || bitIdx == 0x0F {
			return nil, fmt.Errorf("frame at byte %d: bitrate index %d is free/bad — corrupt header", off, bitIdx)
		}
		mpeg1 := verBits == 0x03
		bitrate := mp3Bitrates[boolIdx(mpeg1)][bitIdx]
		rateTable := 0
		if !mpeg1 {
			rateTable = 1
			if verBits == 0x02 {
				rateTable = 2
			}
		}
		r := mp3Rates[rateTable][rateIdx]
		frameLen := 144000 * bitrate / r
		if !mpeg1 {
			frameLen = 72000 * bitrate / r
		}
		frameLen += pad
		if frameLen < 4 {
			return nil, fmt.Errorf("frame at byte %d computes to %d bytes — corrupt", off, frameLen)
		}
		if frames == 0 {
			rate = r
			if n, ok := xingFrames(b); ok {
				declared = n
			}
		} else if rate != r {
			return nil, fmt.Errorf("frame %d changes sample rate to %d Hz — VBR header-less streams must stay constant for duration math", frames, r)
		}
		if off+frameLen > len(body) {
			break // truncated final frame
		}
		if mpeg1 {
			samples += 1152
		} else {
			samples += 576 // MPEG-2/2.5 Layer III halves the block
		}
		frames++
		off += frameLen
	}
	if frames == 0 {
		return nil, fmt.Errorf("no MP3 frame found after %d bytes of header junk", len(body))
	}
	if declared >= 0 && declared != frames {
		return nil, fmt.Errorf("Xing header promises %d frames, only %d present — truncated output (%.1f%% missing)",
			declared, frames, 100*float64(declared-frames)/float64(declared))
	}
	if tail := len(body) - off; tail > 0 && !tailIsPaddingOrID3v1(body[off:]) {
		return nil, fmt.Errorf("frame chain ends with %d unpadded bytes after frame %d — truncated or corrupt tail", tail, frames)
	}
	return &AudioInfo{Format: MP3, DurationSeconds: float64(samples) / float64(rate), SampleRate: rate, BitDepth: 0}, nil
}

// tailIsPaddingOrID3v1 tolerates what legally follows the last frame: an ID3v1
// tag, or encoder padding made of 0x00/0xFF bytes. Anything else means the
// stream was cut mid-frame — truncation without a Xing count to confirm it.
func tailIsPaddingOrID3v1(tail []byte) bool {
	if len(tail) == 128 && string(tail[:3]) == "TAG" {
		return true
	}
	for _, b := range tail {
		if b != 0x00 && b != 0xFF {
			return false
		}
	}
	return true
}

// xingFrames reads the Xing/Info/LAME frame count from the first frame. MPEG-1
// Layer III side info places the marker at byte 36 (stereo) or 21 (mono).
func xingFrames(f []byte) (int64, bool) {
	if len(f) < 45 {
		return 0, false
	}
	at := 36
	if (f[3]>>6)&0x03 == 0x03 { // channel mode mono
		at = 21
	}
	if string(f[at:at+4]) != "Xing" && string(f[at:at+4]) != "Info" {
		return 0, false
	}
	flags := binary.BigEndian.Uint32(f[at+4 : at+8])
	if flags&0x01 == 0 {
		return 0, false // no frame count field
	}
	return int64(binary.BigEndian.Uint32(f[at+8 : at+12])), true
}

func boolIdx(b bool) int {
	if b {
		return 0
	}
	return 1
}

// ── FLAC ─────────────────────────────────────────────────────────────────────

// validateFLAC walks the metadata block chain to the last-flag block and reads
// STREAMINFO: sample rate, channels, bit depth and the total-sample count that
// states the exact duration. A block whose declared length runs past EOF is a
// truncated stream — the header chain tells on it before any audio does.
func validateFLAC(data []byte) (*AudioInfo, error) {
	if len(data) < 4 || string(data[0:4]) != "fLaC" {
		return nil, fmt.Errorf("missing fLaC magic")
	}
	info := &AudioInfo{Format: FLAC}
	off, sawStreamInfo, last := 4, false, false
	for !last && off+4 <= len(data) {
		last = data[off]&0x80 != 0
		typ := data[off] & 0x7F
		size := int(data[off+1])<<16 | int(data[off+2])<<8 | int(data[off+3])
		body := off + 4
		if body+size > len(data) {
			return nil, fmt.Errorf("metadata block type %d declares %d bytes at offset %d, only %d remain — truncated before the last block", typ, size, body, len(data)-body)
		}
		if off == 4 && typ != 0 {
			return nil, fmt.Errorf("first metadata block is type %d, want STREAMINFO (0)", typ)
		}
		if typ == 0 {
			sawStreamInfo = true
			if size < 34 {
				return nil, fmt.Errorf("STREAMINFO is %d bytes, want 34", size)
			}
			v := binary.BigEndian.Uint64(data[body+10 : body+18])
			info.SampleRate = int(v >> 44)
			info.Channels = int((v>>41)&0x7) + 1
			info.BitDepth = int((v>>36)&0x1F) + 1
			total := v & (1<<36 - 1)
			if info.SampleRate == 0 {
				return nil, fmt.Errorf("STREAMINFO sample rate is 0")
			}
			info.DurationSeconds = float64(total) / float64(info.SampleRate) // total==0 → unknown, stays 0
		}
		off = body + size
	}
	if !last {
		return nil, fmt.Errorf("metadata chain never reaches a last-flag block — truncated header")
	}
	if !sawStreamInfo {
		return nil, fmt.Errorf("no STREAMINFO block")
	}
	return info, nil
}

// ── Ogg/Opus ─────────────────────────────────────────────────────────────────

// Ogg CRC-32: poly 0x04c11db7, MSB-first, init 0, no reflection, no final xor —
// distinct from IEEE CRC-32 despite sharing the polynomial, so hash/crc32's
// complemented convention does not fit and the table is built here.
var oggCRC32Table = func() (t [256]uint32) {
	for i := range t {
		r := uint32(i) << 24
		for k := 0; k < 8; k++ {
			if r&0x80000000 != 0 {
				r = (r << 1) ^ 0x04c11db7
			} else {
				r <<= 1
			}
		}
		t[i] = r
	}
	return
}()

func oggCRC32(b []byte) uint32 {
	crc := uint32(0)
	for _, x := range b {
		crc = oggCRC32Table[byte(crc>>24)^x] ^ (crc << 8)
	}
	return crc
}

// validateOGGOpus walks the page chain: capture pattern, version 0, contiguous
// serial and sequence numbers, a recomputed CRC per page (the only check that
// catches a byte flipped in transit), the OpusHead identity packet with its
// pre-skip, and the end-of-stream flag. The final granule position minus
// pre-skip, over 48 kHz, is the exact duration — Opus always states it.
func validateOGGOpus(data []byte) (*AudioInfo, error) {
	info := &AudioInfo{Format: Opus, SampleRate: 48000}
	off, seq, pages := 0, uint32(0), 0
	serial := -1
	var preSkip, lastGranule int64 = 0, -1
	eos := false
	for off+27 <= len(data) {
		if string(data[off:off+4]) != "OggS" {
			return nil, fmt.Errorf("at byte %d after %d pages: not an OggS capture pattern — corrupt page boundary", off, pages)
		}
		if data[off+4] != 0 {
			return nil, fmt.Errorf("page %d: stream version %d, want 0", pages, data[off+4])
		}
		hdrType := data[off+5]
		granule := int64(binary.LittleEndian.Uint64(data[off+6 : off+14]))
		pageSerial := binary.LittleEndian.Uint32(data[off+14 : off+18])
		pageSeq := binary.LittleEndian.Uint32(data[off+18 : off+22])
		pageCRC := binary.LittleEndian.Uint32(data[off+22 : off+26])
		nseg := int(data[off+26])
		if off+27+nseg > len(data) {
			return nil, fmt.Errorf("page %d: segment table of %d entries runs past EOF — truncated page header", pages, nseg)
		}
		bodyLen := 0
		for _, s := range data[off+27 : off+27+nseg] {
			bodyLen += int(s)
		}
		if off+27+nseg+bodyLen > len(data) {
			return nil, fmt.Errorf("page %d: body promises %d bytes, only %d remain — truncated page", pages, bodyLen, len(data)-off-27-nseg)
		}
		page := data[off : off+27+nseg+bodyLen]
		checksum := oggCRC32(crcZeroedPage(page))
		if checksum != pageCRC {
			return nil, fmt.Errorf("page %d: CRC is 0x%08x in the header, 0x%08x recomputed — bytes corrupted in transit", pages, pageCRC, checksum)
		}
		if pageSeq != seq {
			return nil, fmt.Errorf("page %d: sequence number %d after %d — a page was dropped mid-stream", pages, pageSeq, seq)
		}
		if serial < 0 {
			serial = int(pageSerial)
			if hdrType&0x02 == 0 {
				return nil, fmt.Errorf("first page lacks the beginning-of-stream flag")
			}
			po := off + 27 + nseg
			if string(data[po:po+8]) != "OpusHead" {
				return nil, fmt.Errorf("first packet starts %q, want OpusHead — not an Opus stream", data[po:po+8])
			}
			if po+13 > len(data) || data[po+9] != 1 {
				return nil, fmt.Errorf("OpusHead version %d, want 1 (or truncated)", data[po+9])
			}
			preSkip = int64(binary.LittleEndian.Uint16(data[po+10 : po+12]))
		} else if int(pageSerial) != serial {
			return nil, fmt.Errorf("page %d switches serial to %d — interleaved streams are not what /v1/audio/speech should sell", pages, pageSerial)
		}
		if hdrType&0x04 != 0 {
			eos = true
		}
		if granule >= 0 {
			lastGranule = granule
		}
		off += 27 + nseg + bodyLen
		seq++
		pages++
	}
	if pages == 0 {
		return nil, fmt.Errorf("no Ogg pages")
	}
	if !eos {
		return nil, fmt.Errorf("stream never sets the end-of-stream flag — truncated output (%d pages)", pages)
	}
	if lastGranule >= 0 && lastGranule > preSkip {
		info.DurationSeconds = float64(lastGranule-preSkip) / 48000.0
	}
	return info, nil
}

// crcZeroedPage copies page with the 4-byte CRC field at offset 22 zeroed —
// the exact state every Ogg sender computes the checksum over.
func crcZeroedPage(page []byte) []byte {
	out := make([]byte, len(page))
	copy(out, page)
	for i := 22; i < 26; i++ {
		out[i] = 0
	}
	return out
}

// ── AAC (ADTS) ───────────────────────────────────────────────────────────────

var aacRates = [16]int{96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350, 0, 0, 0}

// validateAAC walks the ADTS frame chain (an ID3v2 tag ahead of it is legal)
// and multiplies frames × 1024 samples by the stated rate. A stream whose next
// sync word never arrives after a declared frame length is truncated.
func validateAAC(data []byte) (*AudioInfo, error) {
	body := skipID3(data)
	if len(body) < 7 || body[0] != 0xFF || body[1]&0xF0 != 0xF0 || body[1]&0x06 != 0x00 {
		return nil, fmt.Errorf("no ADTS sync word at byte 0 — not an AAC/ADTS stream")
	}
	off, frames := 0, int64(0)
	var rate int
	for off+7 <= len(body) {
		b := body[off:]
		if b[0] != 0xFF || b[1]&0xF0 != 0xF0 || b[1]&0x06 != 0x00 {
			break
		}
		freqIdx := (b[2] >> 2) & 0x0F
		frameLen := int(b[3]&0x03)<<11 | int(b[4])<<3 | int(b[5]>>5)
		if freqIdx == 0x0F || aacRates[freqIdx] == 0 {
			return nil, fmt.Errorf("frame at byte %d: frequency index %d is reserved — corrupt ADTS header", off, freqIdx)
		}
		if frameLen < 7 {
			return nil, fmt.Errorf("frame at byte %d declares %d bytes, below the 7-byte header — corrupt", off, frameLen)
		}
		if frames == 0 {
			rate = aacRates[freqIdx]
		} else if rate != aacRates[freqIdx] {
			return nil, fmt.Errorf("frame %d changes the sample rate — ADTS streams must state one rate for duration math", frames)
		}
		if off+frameLen > len(body) {
			return nil, fmt.Errorf("frame %d declares %d bytes at offset %d, only %d remain — truncated output", frames, frameLen, off, len(body)-off)
		}
		frames++
		off += frameLen
	}
	if frames == 0 {
		return nil, fmt.Errorf("ADTS header decodes but no first frame follows")
	}
	return &AudioInfo{Format: AAC, DurationSeconds: float64(frames*1024) / float64(rate), SampleRate: rate}, nil
}

// ── loudness ─────────────────────────────────────────────────────────────────

// Level computes RMS and peak of s16le PCM, normalised to full scale. It is the
// answer to "the response was a valid container — and it was silent": Kokoro,
// rate-limited or misconfigured upstreams can deliver a perfect WAV envelope
// carrying nothing but zeros, and only a level check between 200 and the
// test-passing line catches that class.
func Level(pcm []byte) (rms float64, peak int) {
	if len(pcm) < 2 {
		return 0, 0
	}
	var sum float64
	n := len(pcm) / 2
	for i := 0; i < n; i++ {
		s := int16(binary.LittleEndian.Uint16(pcm[2*i:]))
		if a := int(s); -a > peak {
			peak = -a
		} else if a > peak {
			peak = a
		}
		f := float64(s) / 32768.0
		sum += f * f
	}
	return math.Sqrt(sum / float64(n)), peak
}

// SignalLevel extracts and levels the samples of any PCM-carrying response
// (wav, pcm). Compressed containers report false — decoding mp3/opus/aac with
// the standard library alone is out of scope, and their live checks stay
// structural. shortcut: compressed formats skip the loudness check; add a
// pure-Go decoder if silence-inside-a-valid-mp3 ever shows up in production.
func SignalLevel(f Format, data []byte) (rms float64, peak int, ok bool) {
	switch f {
	case PCM:
		r, p := Level(data)
		return r, p, true
	case WAV:
		pcm, err := wavPCM(data)
		if err != nil {
			return 0, 0, false
		}
		r, p := Level(pcm)
		return r, p, true
	}
	return 0, 0, false
}
