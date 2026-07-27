// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2025 The OpenAgent Authors. All Rights Reserved.
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

//go:build !skipCi

package split

import (
	"testing"
)

func TestSplit(t *testing.T) {
	p, err := GetSplitProvider("Markdown")
	if err != nil {
		panic(err)
	}

	text := `# Section 1

Here is a standard Markdown table:

| Header1 | Header2 | Header3 |
|---------|---------|---------|
| A1      | B1      | C1      |
| A2      | B2      | C2      |

# Section 2

This is the content of the second section.

1. **The first point**

   This is the first sentence of the content below the first point.

2. **The second point**

   This is the first sentence of the content below the second point.

A borderless Markdown table:
Some text before

Data1 | Data2
:-----|:-----
More data1 | More data2

There is also an HTML table:
<table>
    <tr>
        <td>Cell 1</td>
        <td>Cell 2</td>
    </tr>
</table>
`

	textSections, err := p.SplitText(text)
	if err != nil {
		t.Fatalf("SplitText: %v", err)
	}
	// Every chunk carries its heading as context — that is what
	// ExtractTablesWithContext is for, and a bare table is not retrievable
	// without knowing which section it came from. Order is the document's.
	targetSections := []string{
		"# Section 1\n\n| Header1 | Header2 | Header3 |\n|---------|---------|---------|\n| A1      | B1      | C1      |\n| A2      | B2      | C2      |",
		"# Section 1\n\nHere is a standard Markdown table:",
		"# Section 2\n\nThis is the content of the second section.",
		"1. **The first point**\n\nThis is the first sentence of the content below the first point.",
		"2. **The second point**\n\nData1 | Data2\n:-----|:-----\nMore data1 | More data2",
		"2. **The second point**\n\n<table>\n<tr>\n<td>Cell 1</td>\n<td>Cell 2</td>\n</tr>\n</table>",
		"2. **The second point**\n\nThis is the first sentence of the content below the second point.\nA borderless Markdown table:\nSome text before\nThere is also an HTML table:",
	}

	// Report WHICH section differs. "did not get the expected result" names
	// nothing, so a failure here used to mean re-deriving the split by hand
	// before you could even tell whether the code or the expectation was wrong.
	if len(textSections) != len(targetSections) {
		t.Errorf("got %d sections, want %d", len(textSections), len(targetSections))
	}
	for i := range targetSections {
		if i >= len(textSections) {
			t.Errorf("section %d missing, want %q", i, targetSections[i])
			continue
		}
		if textSections[i] != targetSections[i] {
			t.Errorf("section %d:\n  got  %q\n  want %q", i, textSections[i], targetSections[i])
		}
	}
	for i := len(targetSections); i < len(textSections); i++ {
		t.Errorf("section %d unexpected: %q", i, textSections[i])
	}
}

func TestExtractMarkdownTree(t *testing.T) {
	text := `# main title

This is the content of the main title.

## sub title 1

This is the content of the sub title 1.

### sub title 1.1

This is the content of the sub title 1.1.

## sub title 2

This is the content of the sub title 2.

Second paragraph.

# another main title

This is the content of the another main title.

## another sub title

This is the content of the another sub title.
`

	headingsMap := ExtractMarkdownTree(text)

	expectedMap := map[string]string{
		"# main title":                                      "This is the content of the main title.",
		"# main title > ## sub title 1":                     "This is the content of the sub title 1.",
		"# main title > ## sub title 1 > ### sub title 1.1": "This is the content of the sub title 1.1.",
		"# main title > ## sub title 2":                     "This is the content of the sub title 2.\nSecond paragraph.",
		"# another main title":                              "This is the content of the another main title.",
		"# another main title > ## another sub title":       "This is the content of the another sub title.",
	}

	// Name the difference. A bare count told you the shape was wrong and
	// nothing about which heading appeared or vanished.
	for key := range headingsMap {
		if _, want := expectedMap[key]; !want {
			t.Errorf("unexpected heading %q = %q", key, headingsMap[key])
		}
	}
	if len(headingsMap) != len(expectedMap) {
		t.Errorf("got %d headings, want %d", len(headingsMap), len(expectedMap))
	}

	for key, expectedValue := range expectedMap {
		if value, exists := headingsMap[key]; !exists {
			t.Errorf("Expected key '%s' not found in result", key)
		} else if value != expectedValue {
			t.Errorf("For key '%s':\nExpected:\n%s\nGot:\n%s", key, expectedValue, value)
		}
	}
}
