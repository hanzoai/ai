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

// HttpxHost represents a single host probed by httpx
type HttpxHost struct {
	Timestamp        string   `json:"timestamp,omitempty"`
	Hash             string   `json:"hash,omitempty"`
	Port             string   `json:"port,omitempty"`
	URL              string   `json:"url"`
	Input            string   `json:"input,omitempty"`
	Title            string   `json:"title,omitempty"`
	Scheme           string   `json:"scheme,omitempty"`
	Webserver        string   `json:"webserver,omitempty"`
	ContentType      string   `json:"content_type,omitempty"`
	Method           string   `json:"method,omitempty"`
	Host             string   `json:"host,omitempty"`
	Path             string   `json:"path,omitempty"`
	FavIconMMH3      string   `json:"favicon_mmh3,omitempty"`
	StatusCode       int      `json:"status_code,omitempty"`
	ContentLength    int      `json:"content_length,omitempty"`
	Words            int      `json:"words,omitempty"`
	Lines            int      `json:"lines,omitempty"`
	Failed           bool     `json:"failed,omitempty"`
	TLSData          string   `json:"tls,omitempty"`
	CSPData          string   `json:"csp,omitempty"`
	VHost            bool     `json:"vhost,omitempty"`
	WebSocket        bool     `json:"websocket,omitempty"`
	Technologies     []string `json:"technologies,omitempty"`
	A                []string `json:"a,omitempty"`
	CNAMEs           []string `json:"cname,omitempty"`
	ChainStatusCodes []int    `json:"chain_status_codes,omitempty"`
}

// HttpxScanResult represents the complete httpx scan result
type HttpxScanResult struct {
	Hosts   []HttpxHost  `json:"hosts"`
	Summary HttpxSummary `json:"summary"`
}

// HttpxSummary provides a summary of the scan results
type HttpxSummary struct {
	TotalHosts   int            `json:"totalHosts"`
	ByStatusCode map[string]int `json:"byStatusCode"`
	ByScheme     map[string]int `json:"byScheme"`
	WithTech     int            `json:"withTech"`
}

type HttpxScanProvider struct {
	httpxPath string
}

// IsHttpxAvailable checks if httpx is available in the system
// Returns true if httpx is available (either through clientId path or system PATH)
func IsHttpxAvailable(clientId string) bool {
	// If clientId is provided, validate the path exists and is executable
	if clientId != "" {
		// Try to run httpx -version to verify it's executable
		cmd := exec.Command(clientId, "-version")
		err := cmd.Run()
		return err == nil
	}

	// Check if httpx is in system PATH
	_, err := exec.LookPath("httpx")
	return err == nil
}

func NewHttpxScanProvider(clientId string) (*HttpxScanProvider, error) {
	path, err := binPath(clientId, "httpx")
	if err != nil {
		return nil, err
	}
	return &HttpxScanProvider{httpxPath: path}, nil
}

func (p *HttpxScanProvider) Scan(target string, command string) (string, error) {
	return scanner{
		name: "httpx", bin: p.httpxPath,
		defaultArgs: "-u %s -json",
		jsonFlags:   []string{"-json", "-jsonl"}, addJSON: "-json",
		targetFlags: []string{"-u", "-target", "-l"}, addTarget: "-u",
		// -t is a thread count here, so it is not one of these.
		offLimits: []string{"-config", "-rl-file"},
	}.run(target, command)
}

func (p *HttpxScanProvider) ParseResult(rawResult string) (string, error) {
	// Parse the JSON output into structured data
	fmt.Printf("%s [httpx] Parsing scan results\n", getHostnamePrefix())

	if rawResult == "" || rawResult == "Scan completed with no hosts found" {
		emptyResult := &HttpxScanResult{
			Hosts: []HttpxHost{},
			Summary: HttpxSummary{
				TotalHosts:   0,
				ByStatusCode: map[string]int{},
				ByScheme:     map[string]int{},
				WithTech:     0,
			},
		}
		jsonBytes, err := json.Marshal(emptyResult)
		if err != nil {
			return "", fmt.Errorf("%s failed to marshal empty httpx result: %v", getHostnamePrefix(), err)
		}
		return string(jsonBytes), nil
	}

	parsedResult := p.parseHttpxOutput(rawResult)

	// Convert to JSON
	jsonBytes, err := json.Marshal(parsedResult)
	if err != nil {
		return "", fmt.Errorf("%s failed to marshal httpx result: %v", getHostnamePrefix(), err)
	}

	hostCount := len(parsedResult.Hosts)
	hostWord := "hosts"
	if hostCount == 1 {
		hostWord = "host"
	}
	fmt.Printf("%s [httpx] Successfully parsed %d %s\n", getHostnamePrefix(), hostCount, hostWord)

	return string(jsonBytes), nil
}

// parseHttpxOutput parses the JSON output from httpx and creates a structured result
func (p *HttpxScanProvider) parseHttpxOutput(output string) *HttpxScanResult {
	result := &HttpxScanResult{
		Hosts: []HttpxHost{},
		Summary: HttpxSummary{
			TotalHosts:   0,
			ByStatusCode: make(map[string]int),
			ByScheme:     make(map[string]int),
			WithTech:     0,
		},
	}

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Parse each JSON line
		var host HttpxHost
		err := json.Unmarshal([]byte(line), &host)
		if err != nil {
			// Skip lines that aren't valid JSON
			continue
		}

		result.Hosts = append(result.Hosts, host)

		// Update summary
		result.Summary.TotalHosts++

		// Count by status code
		if host.StatusCode > 0 {
			statusCodeStr := fmt.Sprintf("%d", host.StatusCode)
			result.Summary.ByStatusCode[statusCodeStr]++
		}

		// Count by scheme
		if host.Scheme != "" {
			result.Summary.ByScheme[host.Scheme]++
		}

		// Count hosts with technologies
		if len(host.Technologies) > 0 {
			result.Summary.WithTech++
		}
	}

	return result
}

// GetResultSummary generates a short summary of the scan result
func (p *HttpxScanProvider) GetResultSummary(result string) string {
	if result == "" {
		return ""
	}

	// Parse the JSON result
	var scanResult HttpxScanResult
	err := json.Unmarshal([]byte(result), &scanResult)
	if err != nil {
		// Log the error but return empty string instead of failing
		fmt.Printf("%s [httpx] Unable to parse scan results for summary: %v\n", getHostnamePrefix(), err)
		return ""
	}

	total := scanResult.Summary.TotalHosts
	if total == 0 {
		return "No hosts found"
	}

	hostWord := "hosts"
	if total == 1 {
		hostWord = "host"
	}

	// Count hosts by status code groups
	success := 0
	for statusCode := range scanResult.Summary.ByStatusCode {
		if strings.HasPrefix(statusCode, "2") {
			success += scanResult.Summary.ByStatusCode[statusCode]
		}
	}

	if success > 0 {
		return fmt.Sprintf("%d %s (%d successful)", total, hostWord, success)
	}

	return fmt.Sprintf("%d %s found", total, hostWord)
}
