// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package txt

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// lowestFreeFd reports the number the kernel would hand out next.
//
// Unix assigns the LOWEST free descriptor, so this rises exactly when
// descriptors are being held open and never falls otherwise. It is a more
// reliable reading than listing /dev/fd, which is not enumerable this way on
// every platform.
func lowestFreeFd(t *testing.T) int {
	t.Helper()
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Skipf("cannot probe descriptors here: %v", err)
	}
	defer f.Close()
	return int(f.Fd())
}

// Downloading a document must not cost a descriptor.
//
// The temp file's handle was never closed, so every remote document parsed left
// one open for the life of the process — and the caller's os.Remove does not
// reclaim the bytes on Unix while a descriptor is still open, so the disk went
// with it. A service parsing documents runs out of descriptors and then fails at
// everything that needs one, including accepting a connection.
//
// Counted rather than reasoned about: the failure is invisible in any single
// call, which is exactly why it survived.
func TestDownloadingADocumentCostsNoDescriptor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("a small document"))
	}))
	defer srv.Close()

	// One call first, so any lazily-initialised transport state is already open
	// and does not read as a leak.
	warm, err := getTempFilePathFromUrl(srv.URL + "/doc.txt")
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	_ = os.Remove(warm)

	before := lowestFreeFd(t)
	const n = 20
	for i := 0; i < n; i++ {
		path, err := getTempFilePathFromUrl(srv.URL + "/doc.txt")
		if err != nil {
			t.Fatalf("download %d: %v", i, err)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("download %d produced no file: %v", i, err)
		}
		_ = os.Remove(path)
	}
	after := lowestFreeFd(t)

	// A few descriptors may move for reasons that are not this loop; one per
	// iteration is the leak.
	if after-before >= n {
		t.Errorf("the lowest free descriptor went %d -> %d across %d downloads — one held per document is the leak", before, after, n)
	}
}

// A download that cannot be written leaves nothing behind: its path is never
// returned, so the caller has nothing to remove.
func TestAFailedDownloadLeavesNoFile(t *testing.T) {
	if _, err := getTempFilePathFromUrl("http://127.0.0.1:1/nothing.txt"); err == nil {
		t.Error("a download from a closed port reported success")
	}
}
