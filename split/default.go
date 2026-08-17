// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2024 The OpenAgent Authors. All Rights Reserved.
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

import (
	"regexp"
	"strings"
	"sync"

	"github.com/pkoukk/tiktoken-go"
)

type DefaultSplitProvider struct {
	TextType string
}

// tokenEncoder returns the ONE shared cl100k encoder the splitter budgets
// with. Resolving an encoding walks model tables and can read files; doing it
// once is the difference between a split and a stall.
var tokenEncoder = sync.OnceValues(func() (*tiktoken.Tiktoken, error) {
	return tiktoken.EncodingForModel("gpt-3.5-turbo")
})

func NewDefaultSplitProvider(textType string) (*DefaultSplitProvider, error) {
	typ := "default"
	if textType != "" {
		typ = textType
	}
	return &DefaultSplitProvider{
		TextType: typ,
	}, nil
}

func (p *DefaultSplitProvider) SplitText(text string) ([]string, error) {
	const maxLength = 210
	sections := []string{}
	var currentSection strings.Builder
	var codeBlock strings.Builder
	inCodeBlock := false
	codeBlockLines := 0
	sectionTokens := 0
	// One encoder for the whole document; resolving it per line was most of a
	// split's wall time.
	enc, encErr := tokenEncoder()
	if encErr != nil {
		return nil, encErr
	}
	tokens := func(t string) int { return len(enc.Encode(t, nil, nil)) }

	lines := strings.Split(text, "\n")
	emptyLineCount := 0

	for _, line := range lines {
		line = strings.TrimSpace(line)

		if line == "" {
			emptyLineCount++
			if emptyLineCount >= 4 && currentSection.Len() > 0 {
				sections = append(sections, currentSection.String())
				currentSection.Reset()
				sectionTokens = 0
			}
			continue
		} else {
			emptyLineCount = 0
		}

		if line == "```" {
			if inCodeBlock {
				inCodeBlock = false
				if codeBlockLines >= 5 {
					if currentSection.Len() > 0 {
						sections = append(sections, currentSection.String())
						currentSection.Reset()
						sectionTokens = 0
					}
					sections = append(sections, codeBlock.String())
				} else {
					currentSection.WriteString(codeBlock.String())
					sectionTokens += tokens(codeBlock.String())
				}
				codeBlock.Reset()
				codeBlockLines = 0
			} else {
				inCodeBlock = true
			}
			continue
		}

		if inCodeBlock {
			codeBlock.WriteString(line + "\n")
			codeBlockLines++
			if codeBlockLines >= 20 {
				if currentSection.Len() > 0 {
					sections = append(sections, currentSection.String())
					currentSection.Reset()
					sectionTokens = 0
				}
				sections = append(sections, codeBlock.String())
				codeBlock.Reset()
				codeBlockLines = 0
			}
			continue
		}

		if p.isSeparator(line) {
			if currentSection.Len() > 0 {
				sections = append(sections, currentSection.String())
				currentSection.Reset()
			}
			currentSection.WriteString(line + "\n")
			sectionTokens = tokens(line)
			continue
		}

		// The budget is a RUNNING count: each line is tokenized once and added.
		// Re-tokenizing the whole accumulated section per line made splitting
		// quadratic in section length — a changelog took a minute to split.
		lineTokens := tokens(line)
		if sectionTokens+lineTokens <= maxLength {
			if currentSection.Len() > 0 {
				currentSection.WriteString("\n")
			}
			currentSection.WriteString(line)
			sectionTokens += lineTokens
		} else {
			if currentSection.Len() > 0 {
				sections = append(sections, currentSection.String())
				currentSection.Reset()
			}
			currentSection.WriteString(line)
			sectionTokens = lineTokens
		}
	}

	if currentSection.Len() > 0 {
		sections = append(sections, currentSection.String())
	}

	return sections, nil
}

func isSectionSeparator(line string) bool {
	// Check for chapter or section titles
	if strings.HasPrefix(line, "Chapter") || strings.HasPrefix(line, "Section") {
		return true
	}
	// Check for numeric bullet points (e.g., "1. ", "2. ")
	matched, _ := regexp.MatchString(`^\d+\.\s`, line)
	return matched
}

func isMarkdownSeparator(line string) bool {
	// Check Markdown titles (1-6 '#' followed by spaces)
	if matched, _ := regexp.MatchString(`^#{1,6}\s+`, line); matched {
		return true
	}
	// Check the numerical sequence number (e.g. "1.", "2. "）
	if matched, _ := regexp.MatchString(`^\d+\.\s`, line); matched {
		return true
	}
	return false
}

func (p *DefaultSplitProvider) isSeparator(line string) bool {
	if p.TextType == "markdown" {
		return isMarkdownSeparator(line)
	} else {
		return isSectionSeparator(line)
	}
}
