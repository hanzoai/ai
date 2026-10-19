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
	"fmt"
	neturl "net/url"
	"os"
	"strings"

	"github.com/hanzoai/ai/i18n"
	"github.com/hanzoai/ai/log"
)

func GetSupportedFileTypes() []string {
	return []string{".txt", ".md", ".yaml", ".csv", ".pdf", ".docx", ".xlsx", ".pptx"}
}

// GetParsedTextFromUrl parses a document THIS SYSTEM stored: a storage URL it
// minted, or a path on this machine it wrote. A string that does not start with
// "http" is read as a local path, which is why an address a request carries never
// comes here — that is GetParsedTextFromAddress.
func GetParsedTextFromUrl(url string, ext string, lang string) (string, error) {
	if !strings.HasPrefix(url, "http") {
		return parseFile(url, ext, lang)
	}
	path, err := getTempFilePathFromUrl(url)
	if err != nil {
		return "", err
	}
	defer removeTemp(path)
	return parseFile(path, ext, lang)
}

// GetParsedTextFromAddress parses the document at an address a REQUEST named — a
// link a person typed into a message, the url of a file a caller asked to embed or
// ingest. It is the one way such an address is read.
//
// It reads http and https and nothing else: a string with no scheme is a path on
// this machine's disk to every reader below, and a request has no business naming
// one. And it reads over proxy.Public, hanzoai/outbound's client, because this
// process sits inside the cluster beside its neighbours and the metadata endpoint;
// a refused address answers outbound.ErrRefused.
func GetParsedTextFromAddress(address string, ext string, lang string) (string, error) {
	u, err := neturl.Parse(strings.TrimSpace(address))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("only an http or https address can be read, not %q", address)
	}
	path, err := getTempFilePathFromAddress(u.String())
	if err != nil {
		return "", err
	}
	defer removeTemp(path)
	return parseFile(path, ext, lang)
}

func removeTemp(path string) {
	if err := os.Remove(path); err != nil {
		log.Error("%v", err.Error())
	}
}

// parseFile reads the document at a local path by the parser its extension names.
func parseFile(path string, ext string, lang string) (string, error) {
	var res string
	var err error
	if ext == "" || ext == ".txt" || ext == ".md" || ext == ".yaml" {
		res, err = getTextFromPlain(path)
	} else if ext == ".csv" {
		res, err = getTextFromCsv(path)
	} else if ext == ".pdf" {
		res, err = getTextFromPdf(path)
	} else if ext == ".docx" {
		res, err = GetTextFromDocx(path, lang)
	} else if ext == ".xlsx" {
		res, err = getTextFromXlsx(path)
	} else if ext == ".pptx" {
		res, err = getTextFromPptx(path)
	} else {
		return "", fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "txt:unsupported file type: %s"), ext))
	}
	if err != nil {
		return "", err
	}

	return res, nil
}
