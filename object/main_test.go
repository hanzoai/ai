//go:build !skipCi

package object

import (
	"fmt"
	"os"
	"path/filepath"
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
// That absence must mean "these did not run", never "these failed". It used to
// be enforced with os.Exit(0) BEFORE m.Run(), which bought that honesty at the
// price of the whole package: every test here was skipped, including the ones
// that touch no database at all, and `go test ./object/` reported a cheerful
// "ok" in 0.1s having executed nothing. That is false assurance, which is worse
// than a red suite — it hid a real defect (the crawl-storage default still
// naming a retired MinIO Service) behind a passing package for as long as it
// existed.
//
// Only TWO tests in this package actually acquire a store, so the gate belongs
// in THEM, not over all 182. They now t.Skip on an absent store — the same
// convention article_test.go and datastore_live_test.go already use — and the
// panics that made a missing database abort the whole binary are t.Skip/t.Fatalf
// instead. So the suite runs, the pure tests report real results, and the
// integration tests still say "did not run" rather than "failed".
//
// The reason is still printed, because a run where the integration tests skipped
// is not the same run as one where they passed. The build tag still works and is
// still the way to exclude them at compile time.
func TestMain(m *testing.M) {
	if reason := probeStore(); reason != "" {
		fmt.Printf("object: integration tests will skip — %s\n", reason)
	}
	os.Exit(m.Run())
}

// requireStore skips the calling test when there is no seeded database.
//
// This is the gate that used to live in TestMain over the whole package. Here it
// applies to exactly the tests that need it, so a missing database stops the
// integration tests and nothing else. Call it as the first statement of any test
// that reads or writes real rows: without it such a test reaches a nil store and
// panics, and a panic aborts the entire package binary — which is how one
// missing database used to hide every other result in this package.
// withStore gives a test a real, empty SQLite store — the same shape the
// controllers helper of this name gives, spelled again because a test helper
// cannot cross a package boundary here: anything both packages shared would have
// to import object, and object's own in-package tests cannot import it back.
//
// requireStore, below, is the other half of the pair: it SKIPS when no seeded
// database is present, which is right for a test that reads existing data and
// wrong for one that wants to prove behaviour.
func withStore(t *testing.T) {
	t.Helper()
	t.Setenv("driverName", "sqlite")
	t.Setenv("dataSourceName", filepath.Join(t.TempDir(), "store.db"))
	InitConfig()
}

func requireStore(t *testing.T) {
	t.Helper()
	if reason := probeStore(); reason != "" {
		t.Skipf("integration test needs a seeded database — %s", reason)
	}
}

// StoreProbeReason exposes probeStore to the object_test package, whose files
// share this test binary but cannot see an unexported identifier. It is declared
// in a _test.go file, so it is not part of the object package's API — the
// standard export_test.go idiom. One probe, two thin wrappers, rather than a
// second copy of the logic that can drift from this one.
func StoreProbeReason() string { return probeStore() }

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
