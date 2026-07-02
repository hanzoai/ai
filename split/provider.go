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

package split

type SplitProvider interface {
	SplitText(text string) ([]string, error)
}

func GetSplitProvider(typ string) (SplitProvider, error) {
	var p SplitProvider
	var err error
	if typ == "Default" {
		p, err = NewDefaultSplitProvider("default")
	} else if typ == "QA" {
		p, err = NewQaSplitProvider()
	} else if typ == "Basic" {
		p, err = NewBasicSplitProvider()
	} else if typ == "Markdown" {
		p, err = NewMarkdownSplitProvider()
	} else if typ == "Recursive" {
		// Character-based recursive splitter (parity with the retired rag-api's
		// RecursiveCharacterTextSplitter). Defaults 1500/100; the RAG module
		// constructs it directly via NewRecursiveSplitProvider to honor the
		// configured RAG_CHUNK_SIZE / RAG_CHUNK_OVERLAP.
		p, err = NewRecursiveSplitProvider(0, -1)
	} else {
		p, err = NewDefaultSplitProvider("default")
	}

	if err != nil {
		return nil, err
	}
	return p, nil
}
