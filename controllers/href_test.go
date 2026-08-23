// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An href in a chat question asks the answer path to go and read a document. The
// question is typed by a person, so the address in it is a request — a path with
// no scheme names this machine's own disk, and an address inside this network
// names our neighbours.
func TestAQuestionCannotNameThisMachine(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("upstream-api-key=sk-real"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, href := range []string{secret, "../../etc/passwd", "file:///etc/passwd",
		"http://169.254.169.254/latest/meta-data/", "http://127.0.0.1:8000/v1/kms"} {
		out, err := refineQuestionTextViaParsingUrlContent(
			`read <a href="`+href+`">this</a> please`, "en")
		if err == nil {
			t.Errorf("href %q was read: %q", href, out)
			continue
		}
		if strings.Contains(out, "sk-real") {
			t.Errorf("href %q leaked the file's contents", href)
		}
	}

	// A question with no href is left exactly as it was.
	q := "what is the capital of France?"
	out, err := refineQuestionTextViaParsingUrlContent(q, "en")
	if err != nil || out != q {
		t.Errorf("a plain question came back as %q, %v", out, err)
	}
}
