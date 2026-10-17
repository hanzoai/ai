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

// mp4.go — a clip's duration and frame size, read from its file.
//
// An MP4 (ISO/IEC 14496-12) is a tree of boxes, each a 32-bit size and a
// four-letter type. The movie header (moov/mvhd) states the movie's timescale and
// duration; each track header (moov/trak/tkhd) states the track's width and
// height as 16.16 fixed point, which a video track sets and an audio track leaves
// zero. Only those boxes are read, wherever moov sits in the file.

import (
	"encoding/binary"
	"errors"
	"io"
)

// mp4Info is what a clip's file says it is.
type mp4Info struct {
	width, height int
	seconds       float64
}

var errNotMP4 = errors.New("no movie header")

// readMP4 reads an MP4 of size bytes from r: its duration and the frame size of
// its first video track.
func readMP4(r io.ReaderAt, size int64) (mp4Info, error) {
	var info mp4Info
	found := false
	err := boxes(r, 0, size, func(kind string, at, n int64) error {
		if kind != "moov" {
			return nil
		}
		found = true
		return boxes(r, at, at+n, func(kind string, at, n int64) error {
			switch kind {
			case "mvhd":
				s, err := mvhd(r, at, n)
				info.seconds = s
				return err
			case "trak":
				if info.width > 0 {
					return nil
				}
				return boxes(r, at, at+n, func(kind string, at, n int64) error {
					if kind != "tkhd" || info.width > 0 {
						return nil
					}
					w, h, err := tkhd(r, at, n)
					info.width, info.height = w, h
					return err
				})
			}
			return nil
		})
	})
	if err != nil {
		return mp4Info{}, err
	}
	if !found {
		return mp4Info{}, errNotMP4
	}
	return info, nil
}

// boxes walks the boxes between start and end, handing each one's type and the
// offset and length of its payload to visit.
func boxes(r io.ReaderAt, start, end int64, visit func(kind string, at, n int64) error) error {
	head := make([]byte, 16)
	for at := start; at+8 <= end; {
		if _, err := r.ReadAt(head[:8], at); err != nil {
			return err
		}
		size := int64(binary.BigEndian.Uint32(head[:4]))
		kind := string(head[4:8])
		body := at + 8
		switch size {
		case 0:
			size = end - at
		case 1:
			if _, err := r.ReadAt(head[8:16], at+8); err != nil {
				return err
			}
			size = int64(binary.BigEndian.Uint64(head[8:16]))
			body = at + 16
		}
		if size < body-at || at+size > end {
			return errors.New("a box runs past its parent")
		}
		if err := visit(kind, body, at+size-body); err != nil {
			return err
		}
		at += size
	}
	return nil
}

// mvhd reads a movie header's duration in seconds.
func mvhd(r io.ReaderAt, at, n int64) (float64, error) {
	b := make([]byte, min(n, 32))
	if _, err := r.ReadAt(b, at); err != nil {
		return 0, err
	}
	var scale, dur uint64
	if b[0] == 1 {
		if len(b) < 32 {
			return 0, errNotMP4
		}
		scale = uint64(binary.BigEndian.Uint32(b[20:24]))
		dur = binary.BigEndian.Uint64(b[24:32])
	} else {
		if len(b) < 20 {
			return 0, errNotMP4
		}
		scale = uint64(binary.BigEndian.Uint32(b[12:16]))
		dur = uint64(binary.BigEndian.Uint32(b[16:20]))
	}
	if scale == 0 {
		return 0, errNotMP4
	}
	return float64(dur) / float64(scale), nil
}

// tkhd reads a track header's width and height in pixels.
func tkhd(r io.ReaderAt, at, n int64) (int, int, error) {
	b := make([]byte, min(n, 92))
	if _, err := r.ReadAt(b, at); err != nil {
		return 0, 0, err
	}
	off := 76
	if len(b) > 0 && b[0] == 1 {
		off = 88
	}
	if len(b) < off+8 {
		return 0, 0, errNotMP4
	}
	w := int(binary.BigEndian.Uint32(b[off:off+4]) >> 16)
	h := int(binary.BigEndian.Uint32(b[off+4:off+8]) >> 16)
	return w, h, nil
}
