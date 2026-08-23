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
