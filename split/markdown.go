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

package split

import (
	"fmt"
	"regexp"
	"strings"
)

type MarkdownSplitProvider struct{}

func NewMarkdownSplitProvider() (*MarkdownSplitProvider, error) {
	return &MarkdownSplitProvider{}, nil
}

// ExtractMarkdownTree returns the sections keyed by breadcrumb. Order lives in
// extractMarkdownSections — the walk is the order.
func ExtractMarkdownTree(markdownText string) map[string]string {
	m, _ := extractMarkdownSections(markdownText)
	return m
}

// extractMarkdownSections walks the document once and returns the sections
// with their keys in DOCUMENT ORDER. A breadcrumb key ("A > B") is a composed
// name, not a substring of the source, so order can only come from the walk
// itself — searching the text for it finds nothing and costs a full scan per
// probe, which on a long changelog turned one file into tens of seconds.
func extractMarkdownSections(markdownText string) (map[string]string, []string) {
	numberedHeadingPattern := regexp.MustCompile(`^(\d+(\.\d+)*\.)\s+(.+)$`)
	hashHeadingPattern := regexp.MustCompile(`^(#{1,6})\s+(.+)$`)

	lines := strings.Split(markdownText, "\n")
	result := make(map[string]string)
	var order []string

	var currentKey string
	var currentContent []string
	var path []string

	// flush records the section that just ended.
	//
	// A document opening with a heading has no preamble, and the old code still
	// wrote result["root"] = "" — a key for content that does not exist. It
	// showed up as a phantom section with no text and made the heading count
	// wrong. A named heading is kept even when its body is empty, because the
	// heading itself is content; "root" is not a heading, it is the absence of
	// one, so with no body there is nothing to record.
	//
	// Also the only copy: these five lines were written twice, once in the loop
	// and once after it.
	flush := func() {
		body := strings.TrimSpace(strings.Join(currentContent, "\n"))
		if currentKey != "" {
			if _, seen := result[currentKey]; !seen {
				order = append(order, currentKey)
			}
			result[currentKey] = body
			return
		}
		if body != "" {
			if _, seen := result["root"]; !seen {
				order = append(order, "root")
			}
			result["root"] = body
		}
	}

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var isHeading bool
		var level int
		var title string

		if m := hashHeadingPattern.FindStringSubmatch(line); m != nil {
			title = fmt.Sprintf("%s %s", m[1], m[2])
			level = len(m[1])
			isHeading = true
		} else if m := numberedHeadingPattern.FindStringSubmatch(line); m != nil {
			title = fmt.Sprintf("%s %s", m[1], m[3])
			level = strings.Count(m[1], ".")
			isHeading = true
		}

		if isHeading {
			flush()

			// update path by level
			if level == len(path)+1 {
				// normal level up
				path = append(path, title)
			} else if level == len(path) {
				// same level, replace the last layer
				path[len(path)-1] = title
			} else if level < len(path) {
				// return to parent level
				path = path[:level-1]
				path = append(path, title)
			} else {
				path = append(path, title)
			}

			currentKey = strings.Join(path, " > ")
			currentContent = []string{}
		} else {
			currentContent = append(currentContent, line)
		}
	}

	flush()

	return result, order
}

func ExtractTablesAndRemainder(markdownText string) (string, []string, error) {
	tables := []string{}

	// Every row pattern below ends in \n, so a table that runs to the end of
	// the input loses its LAST ROW: the row fails to match, stays in the
	// remainder, and comes back out glued to the neighbouring prose chunk. That
	// is how "| A2 | B2 | C2 |" ended up appended to "Here is a standard
	// Markdown table:" — a table silently split across two chunks, which is
	// worse than not splitting it at all.
	//
	// Sections are sliced per heading, so ending without a trailing newline is
	// the common case, not the edge case. One newline makes the last row look
	// like every other row.
	if !strings.HasSuffix(markdownText, "\n") {
		markdownText += "\n"
	}
	remainder := markdownText

	if strings.Contains(markdownText, "|") {
		// Standard Markdown table matching pattern
		borderTablePattern := regexp.MustCompile(`(?m)(?:\n|^)(?:\|.*?\|.*?\|.*?\n)(?:\|(?:\s*[:-]+[-| :]*\s*)\|.*?\n)(?:\|.*?\|.*?\|.*?\n)+`)
		borderTables := borderTablePattern.FindAllString(markdownText, -1)
		tables = append(tables, borderTables...)
		remainder = borderTablePattern.ReplaceAllString(remainder, "\n")

		// Borderless Markdown Table Matching Mode
		noBorderTablePattern := regexp.MustCompile(`(?m)(?:\n|^)(?:\S.*?\|.*?\n)(?:(?:\s*[:-]+[-| :]*\s*).*?\n)(?:\S.*?\|.*?\n)+`)
		noBorderTables := noBorderTablePattern.FindAllString(remainder, -1)
		tables = append(tables, noBorderTables...)
		remainder = noBorderTablePattern.ReplaceAllString(remainder, "\n")
	}

	// If the remaining text contains'<table>'(case ignored), try extracting the HTML table
	if strings.Contains(strings.ToLower(remainder), "<table>") {
		htmlTablePattern := regexp.MustCompile(`(?i)(?:\n|^)\s*(?:(?:<html[^>]*>\s*<body[^>]*>\s*<table[^>]*>[\s\S]*?</table>\s*</body>\s*</html>)|(?:<body[^>]*>\s*<table[^>]*>[\s\S]*?</table>\s*</body>)|(?:<table[^>]*>[\s\S]*?</table>))\s*(?:\n|$)`)
		htmlTables := htmlTablePattern.FindAllString(remainder, -1)
		tables = append(tables, htmlTables...)
		remainder = htmlTablePattern.ReplaceAllString(remainder, "\n")
	}
	return remainder, tables, nil
}

func ExtractTablesWithContext(markdownText string, contextKey string) (string, []string, error) {
	remainder, tables, err := ExtractTablesAndRemainder(markdownText)
	if err != nil {
		return "", nil, err
	}

	tablesWithContext := make([]string, len(tables))
	for i, table := range tables {
		// Trim before joining: the table patterns capture their leading
		// newline, so joining with "\n\n" produced three in a row.
		tablesWithContext[i] = contextKey + "\n\n" + strings.TrimSpace(table)
	}

	return remainder, tablesWithContext, nil
}

func (p *MarkdownSplitProvider) SplitText(text string) ([]string, error) {
	// The walk visits headings in document order, and document order is the
	// only order that means anything: chunks are reassembled into context
	// downstream. (Sorting by strings.Index(text, key) looked equivalent and
	// was not — a breadcrumb key is a composed name, not a substring, so every
	// probe scanned the whole document to find nothing and the order stayed
	// the map's.)
	headingsMap, keys := extractMarkdownSections(text)

	var sections []string

	for _, key := range keys {
		content := headingsMap[key]
		remainder, tables, err := ExtractTablesWithContext(content, key)
		if err != nil {
			return nil, err
		}

		// add tables to sections
		for _, table := range tables {
			sections = append(sections, strings.TrimSpace(table))
		}

		// add text to sections
		if strings.TrimSpace(remainder) != "" {
			textSplitter, err := NewDefaultSplitProvider("markdown")
			if err != nil {
				return nil, err
			}

			textSections, err := textSplitter.SplitText(remainder)
			if err != nil {
				return nil, err
			}

			for _, section := range textSections {
				if strings.TrimSpace(section) != "" {
					sections = append(sections, key+"\n\n"+strings.TrimSpace(section))
				}
			}
		}
	}

	return sections, nil
}
