// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package scan

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// scanner is one external scanner: where its binary is, what it is called in a
// message, and the flags it names a target and structured output with.
//
// The three providers that use it had a Scan of their own, sixty lines each,
// differing only in those flags. What was identical in all three was the part
// worth having once: the validation that keeps a shell metacharacter out of a
// target or a command. Three copies of an injection check is three chances for
// one of them to be relaxed, and no way to see that it has been.
type scanner struct {
	name string // how it is named in a message
	bin  string // the binary to run

	defaultArgs string   // used when the caller supplies no command
	jsonFlags   []string // any of these means structured output was already asked for
	addJSON     string   // appended when none of them was
	targetFlags []string // any of these means the target is already named
	addTarget   string   // used to name it when none of them was

	// offLimits are this tool's flags that write to the disk or run something off
	// it. Per tool, because the same short flag is not the same thing twice: -t is
	// nuclei's templates and httpx's thread count. writesFiles is the part every
	// tool shares.
	offLimits []string

	// emptyResult is what a scan that found nothing says, because each parser
	// recognises its own words for it.
	emptyResult string

	// pinned are flags whose value is fixed. ZAP writes its findings through
	// -quickout and this reads them from stdout, so that flag is how the feature
	// works and /dev/stdout is the only place it may point.
	pinned map[string]string
}

// writesFiles are the flags that send a tool's output to the disk. Every scanner
// here is read from STDOUT, so one of these breaks the feature it is used in —
// which is what makes refusing them safe rather than a judgement call.
var writesFiles = []string{
	"-oN", "-oX", "-oS", "-oG", "-oA", "-o", "-output",
	"-sr", "-srd", "-store-response", "-store-response-dir",
	"-or", "-ot", "-stylesheet",
}

// argv validates the target and the command, and renders what the scanner runs.
//
// A target may not carry a shell metacharacter or a dollar; a command may carry
// a dollar and no metacharacter. That difference is deliberate — a command is
// written by an operator and may reference a variable, a target arrives from a
// request — and it is stated once here rather than three times.
func (s scanner) argv(target, command string) ([]string, error) {
	if target == "" {
		return nil, fmt.Errorf("%s scan target cannot be empty", getHostnamePrefix())
	}
	target = strings.TrimSpace(target)
	if strings.ContainsAny(target, ";&|`$") {
		return nil, fmt.Errorf("%s invalid characters in scan target", getHostnamePrefix())
	}

	if command == "" {
		command = s.defaultArgs
	}
	command = strings.TrimSpace(command)
	if strings.ContainsAny(command, ";&|`") {
		return nil, fmt.Errorf("%s invalid characters in scan command", getHostnamePrefix())
	}
	if flag := s.reachesTheFilesystem(command); flag != "" {
		return nil, fmt.Errorf("%s scan command may not use %s", getHostnamePrefix(), flag)
	}
	if flag, want, got := s.movedAPinnedValue(command); flag != "" {
		return nil, fmt.Errorf("%s scan command may only use %s %s, not %s",
			getHostnamePrefix(), flag, want, got)
	}

	if s.addJSON != "" {
		asked := false
		for _, f := range s.jsonFlags {
			if strings.Contains(command, f) {
				asked = true
				break
			}
		}
		if !asked {
			command = command + " " + s.addJSON
		}
	}

	// The target goes where the command says, or after it when the command does
	// not say.
	if strings.Contains(command, "%s") {
		return strings.Fields(strings.Replace(command, "%s", target, -1)), nil
	}
	args := strings.Fields(command)
	for _, f := range s.targetFlags {
		if contains(args, f) {
			return args, nil
		}
	}
	if s.addTarget == "" {
		return append(args, target), nil
	}
	return append(args, s.addTarget, target), nil
}

// run renders the argv and runs it, and reports what the scanner wrote.
//
// A non-zero exit is not on its own a failure: these tools return one for a scan
// that found nothing to report as readily as for a scan that broke, so what
// decides it is whether anything was written.
func (s scanner) run(target, command string) (string, error) {
	args, err := s.argv(target, command)
	if err != nil {
		return "", err
	}

	cmd := exec.Command(s.bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	fmt.Printf("%s [%s] Executing %s scan: %s %s\n", getHostnamePrefix(), s.name, s.name, s.bin, strings.Join(args, " "))
	if err := cmd.Run(); err != nil {
		if stdout.Len() == 0 {
			return "", fmt.Errorf("%s %s scan failed: %v, stderr: %s", getHostnamePrefix(), s.name, err, stderr.String())
		}
		fmt.Printf("%s [%s] Scan completed with warnings: %v\n", getHostnamePrefix(), s.name, err)
	}
	fmt.Printf("%s [%s] Scan completed successfully\n", getHostnamePrefix(), s.name)

	if out := stdout.String(); out != "" {
		return out, nil
	}
	if s.emptyResult != "" {
		return s.emptyResult, nil
	}
	return "Scan completed with no hosts found", nil
}

// binPath answers where a scanner's binary is: the path the caller gave, or the
// one on PATH, or a refusal that names what is missing.
//
// Four constructors had this, each naming its own tool three times in the same
// sentence, which is three chances for one of them to name a different tool than
// the one it went looking for.
func binPath(given, bin string) (string, error) {
	if given != "" {
		return given, nil
	}
	found, err := exec.LookPath(bin)
	if err != nil {
		return "", fmt.Errorf("%s %s not found in system PATH, please specify the path to %s binary", getHostnamePrefix(), bin, bin)
	}
	return found, nil
}

// reachesTheFilesystem answers which of those a command uses, if any.
//
// Matched on the flag, before its value and before the target is substituted, so
// "-oN", "-oN/tmp/x" and "-oN=/tmp/x" are one thing.
func (s scanner) reachesTheFilesystem(command string) string {
	denied := append(append([]string{}, writesFiles...), s.offLimits...)
	for _, field := range strings.Fields(command) {
		if !strings.HasPrefix(field, "-") {
			continue
		}
		flag := field
		if i := strings.IndexAny(flag, "=:"); i > 0 {
			flag = flag[:i]
		}
		bare := strings.TrimLeft(flag, "-")
		for _, d := range denied {
			// A long flag is spelled with one dash or two, and a value may be
			// attached with no space at all.
			want := strings.TrimLeft(d, "-")
			if bare == want || (len(want) > 1 && strings.HasPrefix(bare, want)) {
				return d
			}
		}
	}
	return ""
}

// movedAPinnedValue answers which pinned flag a command points somewhere else.
func (s scanner) movedAPinnedValue(command string) (flag, want, got string) {
	if len(s.pinned) == 0 {
		return "", "", ""
	}
	fields := strings.Fields(command)
	for i, field := range fields {
		name, value := field, ""
		if j := strings.IndexAny(name, "="); j > 0 {
			name, value = name[:j], name[j+1:]
		} else if i+1 < len(fields) {
			value = fields[i+1]
		}
		for pin, only := range s.pinned {
			if strings.TrimLeft(name, "-") != strings.TrimLeft(pin, "-") {
				continue
			}
			if value != only {
				return pin, only, value
			}
		}
	}
	return "", "", ""
}
