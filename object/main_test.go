//go:build !skipCi

package object

import (
	"fmt"
	"os"
	"testing"
)

// The tests in this package are INTEGRATION tests: they read and write real
// rows through the configured adapter, and several also call a live model
// endpoint. They were written to be excluded with `-tags skipCi`, but nothing
// in the repo passes that tag, so `go test ./...` ran them against a database
// that is not there. Every one of them then dereferenced the nil entity its
// query did not return, and a panic in a test aborts the whole package binary —
// which is why one failure (TestTranslateArticle) was all anyone ever saw, with
// TestUpdateStoreFolders and TestCrawlStorageEndpoint hidden behind it.
//
// This gate makes the absence of a database mean "these did not run" instead of
// "these failed", so the package is honest in both environments: with a
// configured database every test runs exactly as before, and without one the
// suite reports no results rather than a crash. The build tag still works and is
// still the way to exclude them at compile time.
func TestMain(m *testing.M) {
	if reason := probeStore(); reason != "" {
		fmt.Printf("object: skipping integration tests — %s\n", reason)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// probeStore returns why the integration environment is unusable, or "" when it
// is ready. InitConfig panics on a missing config file rather than returning an
// error, so the recover is the only way to ask the question.
func probeStore() (reason string) {
	defer func() {
		if r := recover(); r != nil {
			reason = fmt.Sprintf("configuration is not loadable: %v", r)
		}
	}()

	InitConfig()

	store, err := getStore("admin", "default")
	switch {
	case err != nil:
		return fmt.Sprintf("the store is not readable: %v", err)
	case store == nil:
		return "store admin/default is absent; the tests need a seeded database"
	}
	return ""
}
