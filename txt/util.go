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

package txt

import (
	"io"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/hanzoai/ai/util"
)

// getTempFilePathFromUrl downloads a document to a temp file and returns its
// path. The caller removes the file; this closes the handle.
//
// It used to do neither. The handle stayed open for the life of the process, so
// every remote document parsed leaked a descriptor — and on Unix the caller's
// os.Remove does not reclaim the bytes while a descriptor is still open, so the
// disk went with it. A service parsing documents runs out of descriptors and then
// fails at everything that needs one, including accepting a connection.
//
// A copy that fails takes the file with it. Its path is never returned, so the
// caller has nothing to remove and would leave it behind.
func getTempFilePathFromUrl(url string) (string, error) {
	buffer, err := util.DownloadFile(url)
	if err != nil {
		return "", err
	}

	file, err := ioutil.TempFile("", filepath.Base(url))
	if err != nil {
		return "", err
	}
	defer file.Close()

	if _, err = io.Copy(file, buffer); err != nil {
		_ = os.Remove(file.Name())
		return "", err
	}

	return file.Name(), nil
}
