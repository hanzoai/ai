// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package audio

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// leftBehind counts this conversion's temp files still in the temp directory.
func leftBehind(t *testing.T) int {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "cloud-audio-*"))
	if err != nil {
		t.Fatal(err)
	}
	return len(matches)
}

// A video's audio comes back, and neither the video nor the audio is still in the
// temp directory afterwards. Both files are made per call, so anything left is
// left once per upload.
func TestTakingTheAudioOutOfAVideo(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed here")
	}
	before := leftBehind(t)

	// A second of test pattern with a tone over it.
	video := filepath.Join(t.TempDir(), "in.mp4")
	make := exec.Command("ffmpeg", "-f", "lavfi", "-i", "testsrc=s=64x64:d=1",
		"-f", "lavfi", "-i", "sine=f=440:d=1", "-shortest", "-y", video)
	if out, err := make.CombinedOutput(); err != nil {
		t.Skipf("could not build a test video: %v: %s", err, out)
	}
	source, err := os.ReadFile(video)
	if err != nil {
		t.Fatal(err)
	}

	audio, err := GetAudioFromVideo(bytes.NewBuffer(source))
	if err != nil {
		t.Fatalf("taking the audio out: %v", err)
	}
	if audio.Len() == 0 {
		t.Error("the audio came back empty")
	}
	// An MPEG audio frame starts 0xFF Ex, or the file opens with an ID3 tag.
	head := audio.Bytes()[:3]
	if !(head[0] == 0xFF && head[1]&0xE0 == 0xE0) && string(head) != "ID3" {
		t.Errorf("what came back does not begin like audio: % x", head)
	}

	if after := leftBehind(t); after != before {
		t.Errorf("a conversion left %d temp files behind", after-before)
	}
}

// And a file that is not a video is an error rather than an answer, with nothing
// left in the temp directory either.
func TestSomethingThatIsNotAVideo(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed here")
	}
	before := leftBehind(t)
	for i := 0; i < 3; i++ {
		if _, err := GetAudioFromVideo(bytes.NewBuffer([]byte("not a video"))); err == nil {
			t.Fatal("a file that is not a video converted")
		}
	}
	if after := leftBehind(t); after != before {
		t.Errorf("three refusals left %d temp files behind", after-before)
	}
}
