// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2025 The OpenAgent Authors. All Rights Reserved.
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

package scan

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// SubfinderSubdomain represents a single subdomain found by Subfinder
type SubfinderSubdomain struct {
	Host   string `json:"host"`
	Input  string `json:"input"`
	Source string `json:"source,omitempty"`
}

// SubfinderScanResult represents the complete Subfinder scan result
type SubfinderScanResult struct {
	Subdomains []SubfinderSubdomain `json:"subdomains"`
	Summary    SubfinderSummary     `json:"summary"`
}

// SubfinderSummary provides a summary of the scan results
type SubfinderSummary struct {
	TotalSubdomains int            `json:"totalSubdomains"`
	BySource        map[string]int `json:"bySource"`
}

type SubfinderScanProvider struct {
	subfinderPath string
}

// IsSubfinderAvailable checks if subfinder is available in the system
// Returns true if subfinder is available (either through clientId path or system PATH)
func IsSubfinderAvailable(clientId string) bool {
	// If clientId is provided, validate the path exists and is executable
	if clientId != "" {
		// Try to run subfinder -version to verify it's executable
		cmd := exec.Command(clientId, "-version")
		err := cmd.Run()
		return err == nil
	}

	// Check if subfinder is in system PATH
	_, err := exec.LookPath("subfinder")
	return err == nil
}

func NewSubfinderScanProvider(clientId string) (*SubfinderScanProvider, error) {
	path, err := binPath(clientId, "subfinder")
	if err != nil {
		return nil, err
	}
	return &SubfinderScanProvider{subfinderPath: path}, nil
}

func (p *SubfinderScanProvider) Scan(target string, command string) (string, error) {
	return scanner{
		name: "subfinder", bin: p.subfinderPath,
		defaultArgs: "-d %s -json",
		jsonFlags:   []string{"-json", "-oJ"}, addJSON: "-json",
		targetFlags: []string{"-d", "-domain", "-dL"}, addTarget: "-d",
		offLimits: []string{"-config", "-pc", "-provider-config"},
	}.run(target, command)
}

func (p *SubfinderScanProvider) ParseResult(rawResult string) (string, error) {
	// Parse the JSON output into structured data
	fmt.Printf("%s [Subfinder] Parsing scan results\n", getHostnamePrefix())

	if rawResult == "" || rawResult == "Scan completed with no subdomains found" {
		emptyResult := &SubfinderScanResult{
			Subdomains: []SubfinderSubdomain{},
			Summary: SubfinderSummary{
				TotalSubdomains: 0,
				BySource:        map[string]int{},
			},
		}
		jsonBytes, err := json.Marshal(emptyResult)
		if err != nil {
			return "", fmt.Errorf("%s failed to marshal empty subfinder result: %v", getHostnamePrefix(), err)
		}
		return string(jsonBytes), nil
	}

	parsedResult := p.parseSubfinderOutput(rawResult)

	// Convert to JSON
	jsonBytes, err := json.Marshal(parsedResult)
	if err != nil {
		return "", fmt.Errorf("%s failed to marshal subfinder result: %v", getHostnamePrefix(), err)
	}

	subdomainCount := len(parsedResult.Subdomains)
	subdomainWord := "subdomains"
	if subdomainCount == 1 {
		subdomainWord = "subdomain"
	}
	fmt.Printf("%s [Subfinder] Successfully parsed %d %s\n", getHostnamePrefix(), subdomainCount, subdomainWord)

	return string(jsonBytes), nil
}

// parseSubfinderOutput parses the JSON output from subfinder and creates a structured result
func (p *SubfinderScanProvider) parseSubfinderOutput(output string) *SubfinderScanResult {
	result := &SubfinderScanResult{
		Subdomains: []SubfinderSubdomain{},
		Summary: SubfinderSummary{
			TotalSubdomains: 0,
			BySource:        make(map[string]int),
		},
	}

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Parse each JSON line
		var subdomain SubfinderSubdomain
		err := json.Unmarshal([]byte(line), &subdomain)
		if err != nil {
			// Skip lines that aren't valid JSON
			continue
		}

		result.Subdomains = append(result.Subdomains, subdomain)

		// Update summary
		result.Summary.TotalSubdomains++
		if subdomain.Source != "" {
			result.Summary.BySource[subdomain.Source]++
		}
	}

	return result
}

// GetResultSummary generates a short summary of the scan result
func (p *SubfinderScanProvider) GetResultSummary(result string) string {
	if result == "" {
		return ""
	}

	// Parse the JSON result
	var scanResult SubfinderScanResult
	err := json.Unmarshal([]byte(result), &scanResult)
	if err != nil {
		// Log the error but return empty string instead of failing
		fmt.Printf("%s [Subfinder] Unable to parse scan results for summary: %v\n", getHostnamePrefix(), err)
		return ""
	}

	total := scanResult.Summary.TotalSubdomains
	if total == 0 {
		return "No subdomains found"
	}

	subdomainWord := "subdomains"
	if total == 1 {
		subdomainWord = "subdomain"
	}

	// Count unique sources
	sourceCount := len(scanResult.Summary.BySource)
	if sourceCount > 0 {
		sourceWord := "sources"
		if sourceCount == 1 {
			sourceWord = "source"
		}
		return fmt.Sprintf("%d %s from %d %s", total, subdomainWord, sourceCount, sourceWord)
	}

	return fmt.Sprintf("%d %s found", total, subdomainWord)
}
