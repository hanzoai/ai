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

package util

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/proxy"
)

func DownloadFile(url string) (*bytes.Buffer, error) {
	httpClient := proxy.GetHttpClient(url)

	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	fileBuffer := bytes.NewBuffer(nil)
	_, err = io.Copy(fileBuffer, resp.Body)
	if err != nil {
		return nil, err
	}

	return fileBuffer, nil
}

// downloadMaxmindFiles downloads MaxMind database files from GitHub
func downloadMaxmindFiles(cityExists, asnExists bool) {
	frontendBaseDir := conf.GetConfigString("frontendBaseDir")

	// GitHub repo for the data files
	repoURL := "https://github.com/hanzoai/ai-data"

	// Helper function to download and save a file
	downloadAndSave := func(filename string) error {
		filePath := filepath.Join(frontendBaseDir, "data", filename+".mmdb")
		fileUrl := filepath.Join(repoURL, "raw", "master", filename+".mmdb")

		EnsureFileFolderExists(filePath)

		log.Info("Downloading %s database from %s", filename, fileUrl)
		buffer, err := DownloadFile(fileUrl)
		if err != nil {
			return err
		}

		// Write buffer to file
		file, err := os.Create(filePath)
		if err != nil {
			return err
		}
		defer file.Close()

		_, err = io.Copy(file, buffer)
		if err != nil {
			return err
		}

		return nil
	}

	if !cityExists {
		cityErr := downloadAndSave("GeoLite2-City")
		if cityErr != nil {
			fmt.Println("Failed to download GeoLite2-City database")
		}
	}

	if !asnExists {
		asnErr := downloadAndSave("GeoLite2-ASN")
		if asnErr != nil {
			fmt.Println("Failed to download GeoLite2-ASN database")
		}
	}
	// Update status in util package
	MaxmindDownloadInProgress = false

	if err := InitMaxmindDb(); err != nil {
		panic("Failed to initialize MaxMind database")
	}
}

// InitMaxmindFiles checks if MaxMind database files exist and downloads them if needed
func InitMaxmindFiles() {
	frontendBaseDir := conf.GetConfigString("frontendBaseDir")

	cityDbPath := filepath.Join(frontendBaseDir, "data", "GeoLite2-City.mmdb")
	asnDbPath := filepath.Join(frontendBaseDir, "data", "GeoLite2-ASN.mmdb")

	cityDbPathAlt := filepath.Join(frontendBaseDir, "..", "data", "GeoLite2-City.mmdb")
	asnDbPathAlt := filepath.Join(frontendBaseDir, "..", "data", "GeoLite2-ASN.mmdb")

	// Check if files exist in either location
	cityExists := FileExist(cityDbPath) || FileExist(cityDbPathAlt)
	asnExists := FileExist(asnDbPath) || FileExist(asnDbPathAlt)

	// If both files exist, we're done
	if cityExists && asnExists {
		return
	}

	MaxmindDownloadInProgress = true

	go downloadMaxmindFiles(cityExists, asnExists)
}
