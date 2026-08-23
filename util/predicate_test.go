// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package util

import (
	"os"
	"path/filepath"
	"testing"

	iam "github.com/hanzoai/ai/internal/iam"
)

// A scan names the machine it should run on, and every instance asks this whether
// that machine is itself. Answering yes twice runs the job twice; answering no
// everywhere runs it nowhere.
func TestWhichMachineAScanNames(t *testing.T) {
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	mine := []string{"localhost", "127.0.0.1", "::1", "127.0.0.53"}
	for _, target := range mine {
		got, err := MatchTargetWithMachine(target, host)
		if err != nil {
			t.Fatalf("%s: %v", target, err)
		}
		if !got {
			t.Errorf("%s names this machine and did not match", target)
		}
	}

	// A hostname matches regardless of case; the DNS it came from does not care.
	for _, target := range []string{host, upper(host)} {
		got, err := MatchTargetWithMachine(target, host)
		if err != nil {
			t.Fatal(err)
		}
		if !got {
			t.Errorf("%q is this machine's own name and did not match", target)
		}
	}

	for _, target := range []string{"203.0.113.7", "someone-elses-host", ""} {
		got, err := MatchTargetWithMachine(target, host)
		if err != nil {
			t.Fatalf("%s: %v", target, err)
		}
		if got {
			t.Errorf("%q is not this machine and matched", target)
		}
	}
}

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}

// A guest is stamped two ways and either alone is authoritative — a session copy
// or a token claim may carry one without the other. A caller with no user at all
// is a different state, and saying "anonymous" for it would let an unauthenticated
// request through a door that admits guests.
func TestRecognisingAGuest(t *testing.T) {
	if IsAnonymousUser(nil) {
		t.Error("no user at all was called a guest")
	}
	for _, u := range []*iam.User{
		{Type: "anonymous-user", Name: "alice"},
		{Type: "normal-user", Name: "u-12345678"},
		{Type: "anonymous-user", Name: "u-12345678"},
	} {
		if !IsAnonymousUser(u) {
			t.Errorf("%+v carries a guest's mark and was not recognised", u)
		}
	}
	if IsAnonymousUser(&iam.User{Type: "normal-user", Name: "alice"}) {
		t.Error("a signed-in person was called a guest")
	}

	// The name alone: u- and eight more.
	for _, name := range []string{"u-12345678"} {
		if !IsAnonymousUserByUsername(name) {
			t.Errorf("%q is a guest's name and was not recognised", name)
		}
	}
	for _, name := range []string{"", "u-", "u-1234567", "u-123456789", "alice", "user-1234"} {
		if IsAnonymousUserByUsername(name) {
			t.Errorf("%q is not a guest's name and was", name)
		}
	}
}

func TestTheVideoRole(t *testing.T) {
	if IsVideoNormalUser(nil) {
		t.Error("no user at all carried a role")
	}
	if !IsVideoNormalUser(&iam.User{Type: UserTypeVideoNormalUser}) {
		t.Error("the role was not recognised")
	}
	if IsVideoNormalUser(&iam.User{Type: "normal-user"}) {
		t.Error("a role that is not this one was recognised as it")
	}
}

// Small readers, on values that arrive from a request.
func TestReadingAPath(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"report.pdf", "report"},
		{"archive.tar.gz", "archive.tar"},
		{"noext", "noext"},
		{".gitignore", ""},
		{"", ""},
		{"a/b/c.txt", "a/b/c"},
	} {
		if got := RemoveExt(c.in); got != c.want {
			t.Errorf("RemoveExt(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	dir := t.TempDir()
	file := filepath.Join(dir, "x.txt")
	if FileExist(file) {
		t.Error("a file that was never written exists")
	}
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !FileExist(file) {
		t.Error("a file that was written does not exist")
	}
	if got := GetPath(file); got != dir {
		t.Errorf("GetPath(%q) = %q, want %q", file, got, dir)
	}
}
