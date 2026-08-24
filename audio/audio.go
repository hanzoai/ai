// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2023 The OpenAgent Authors. All Rights Reserved.
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

package audio

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"strings"
)

func GetAudioFromVideo(inputBuffer *bytes.Buffer) (*bytes.Buffer, error) {
	tmpInputFile, err := os.CreateTemp("", "cloud-audio-*.mp4")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmpInputFile.Name())

	_, err = io.Copy(tmpInputFile, inputBuffer)
	if err != nil {
		return nil, err
	}
	tmpInputFile.Close()

	tmpOutputFileName := strings.Replace(tmpInputFile.Name(), ".mp4", ".mp3", 1)

	// The removal is registered before ffmpeg runs, not after it has worked.
	// ffmpeg leaves a partial file behind when it fails, and every return between
	// here and the end used to be a file left in the temp directory — one per
	// upload that would not convert.
	defer os.Remove(tmpOutputFileName)

	cmd := exec.Command("ffmpeg", "-i", tmpInputFile.Name(), "-q:a", "0", "-map", "a", tmpOutputFileName)
	if err = cmd.Run(); err != nil {
		return nil, err
	}

	tmpOutputFile, err := os.Open(tmpOutputFileName)
	if err != nil {
		return nil, err
	}
	// And the handle is closed however the copy below ends. Registered after it
	// was opened, so it also runs BEFORE the removal above.
	defer tmpOutputFile.Close()

	outputBuffer := bytes.NewBuffer(nil)
	if _, err = io.Copy(outputBuffer, tmpOutputFile); err != nil {
		return nil, err
	}

	return outputBuffer, nil
}
