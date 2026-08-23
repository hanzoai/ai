// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package object

import (
	"testing"

	"github.com/hanzoai/ai/util"
)

// The repository to read comes from the request, so whose credential reads it has
// to be the requester's — otherwise the platform's own access is spent on a
// target the caller chose, and whatever it can reach lands in the caller's index.
func TestARepositoryIsReadWithTheAskersCredential(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp-platform")

	if got := resolveGitHubToken("", "acme"); got != "" {
		t.Errorf("a tenant's ingest was given %q", got)
	}
	if got := resolveGitHubToken("ghp-theirs", "acme"); got != "ghp-theirs" {
		t.Errorf("a tenant's own credential was not used: %q", got)
	}
	// The reserved org is the platform, so its ingests reach the platform's repos.
	if got := resolveGitHubToken("", util.AdminOrg); got != "ghp-platform" {
		t.Errorf("the platform's own ingest got %q", got)
	}
}
