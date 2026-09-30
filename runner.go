package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// runResult contains the parts of a Codex invocation that matter to the
// failover loop. Exec output is streamed to the terminal while also being
// retained so a quota error can be recognized after the process exits.
type runResult struct {
	err             error
	output          string
	threadID        string
	interactiveExit bool
}

// cmdRun runs Codex through the profile manager. It intentionally does not
// switch profiles preemptively: the user's current session stays untouched
// until Codex actually reports a usage/rate limit and the user approves a
// failover.
func cmdRun(p *Paths, s *Store, args []string) error {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if _, err := exec.LookPath("codex"); err != nil {
		return fmt.Errorf("`codex` CLI not found on PATH")
	}
	if s.Active == "" {
		return fmt.Errorf("no active profile — run `cx switch <name>` first")
	}
	if len(args) > 0 && isUnsupportedRunCommand(args[0]) {
		return fmt.Errorf("`cx run` supports the interactive CLI and `codex exec`; it cannot resume `codex %s` automatically", args[0])
	}

	currentArgs := append([]string(nil), args...)
	var threadID string
	for {
		result := runCodexProcess(currentArgs)
		if !isQuotaFailure(result) {
			return result.err
		}

		if hasEphemeralFlag(currentArgs) {
			return fmt.Errorf("Codex hit a usage limit, but this run used --ephemeral so its session cannot be resumed")
		}
		if result.threadID != "" {
			threadID = result.threadID
		}

		resume, selected, err := promptForProfileSwitch(p, s, s.Active, os.Stdin, os.Stdout)
		if err != nil {
			return err
		}
		if !resume {
			if result.err != nil {
				return result.err
			}
			return errors.New("Codex stopped after reporting a usage limit")
		}
		if err := switchTo(p, s, selected); err != nil {
			return fmt.Errorf("switch profile for resume: %w", err)
		}

		currentArgs = resumeArgs(currentArgs, threadID)
		fmt.Printf("Resuming the session with profile %q.\n", selected)
	}
}

func isUnsupportedRunCommand(first string) bool {
	switch first {
	case "", "exec", "e", "resume":
		return false
	default:
		return true
	}
}

func runCodexProcess(args []string) runResult {
	cmd := exec.Command("codex", args...)
	cmd.Stdin = os.Stdin
	if !isExecInvocation(args) {
		// The interactive TUI must see the real terminal. Sending it through
		// io.MultiWriter would give the child a pipe instead of a TTY and can
		// disable or corrupt the terminal UI. Interactive exits are treated as
		// candidates; the confirmation prompt below remains the user's gate.
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err := cmd.Run()
		return runResult{err: err, interactiveExit: isRecoverableInteractiveExit(err)}
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = io.MultiWriter(os.Stdout, &stdout)
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)
	err := cmd.Run()

	output := stdout.String() + "\n" + stderr.String()
	return runResult{
		err:      err,
		output:   output,
		threadID: extractThreadID(stdout.String()),
	}
}

func isExecInvocation(args []string) bool {
	return len(args) > 0 && (args[0] == "exec" || args[0] == "e")
}

func isRecoverableInteractiveExit(err error) bool {
	if err == nil {
		return false
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	code := exitErr.ExitCode()
	return code >= 1 && code != 130 && code != 143
}

func isQuotaFailure(result runResult) bool {
	if result.interactiveExit {
		return true
	}
	if result.err == nil && result.output == "" {
		return false
	}
	text := strings.ToLower(result.output)
	for _, marker := range []string{
		"you've hit your usage limit",
		"you have hit your usage limit",
		"usage limit",
		"rate limit reached",
		"rate limit exceeded",
		"rate_limit_reached",
		"rate_limit_exceeded",
		"too many requests",
		"resource_exhausted",
		"insufficient_quota",
		"quota exceeded",
		"429 too many",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func hasEphemeralFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--ephemeral" {
			return true
		}
	}
	return false
}

// extractThreadID reads the stable thread.started event emitted by
// `codex exec --json`. Plain interactive output does not expose this ID, so
// the resume path falls back to Codex's --last selection in that case.
func extractThreadID(output string) string {
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		var event struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
		}
		if err := json.Unmarshal([]byte(scanner.Text()), &event); err != nil {
			continue
		}
		if event.Type == "thread.started" && event.ThreadID != "" {
			return event.ThreadID
		}
	}
	return ""
}

// resumeArgs preserves the useful execution settings from the original
// invocation but deliberately drops its original prompt. Codex's resume
// subcommands restore the saved conversation and continue it under the newly
// selected auth profile.
func resumeArgs(original []string, threadID string) []string {
	nonInteractive := len(original) > 0 && (original[0] == "exec" || original[0] == "e")
	command := "resume"
	if nonInteractive {
		command = "exec"
	}

	args := []string{command}
	args = append(args, preservedCodexFlags(original, nonInteractive)...)
	if nonInteractive {
		args = append(args, "resume")
	}
	if threadID != "" {
		args = append(args, threadID)
	} else {
		args = append(args, "--last")
	}
	if nonInteractive {
		// `codex exec resume` needs a prompt to start the resumed turn. A
		// generic continuation instruction keeps this wrapper usable even
		// when the original prompt came from a pipe that is now exhausted.
		args = append(args, "Continue the task from where it stopped. Pick up from the last incomplete step and finish it.")
	}
	return args
}

// These are the Codex options whose values or behavior are useful on a
// resumed session. Keeping an allowlist avoids accidentally replaying the
// original prompt or a command-specific positional argument.
var codexValueFlags = map[string]bool{
	"-C": true, "--cd": true,
	"-m": true, "--model": true,
	"-p": true, "--profile": true,
	"-s": true, "--sandbox": true,
	"-c": true, "--config": true,
	"-o": true, "--output-last-message": true,
	"--output-schema": true,
	"--thread-source": true,
	"--add-dir":       true,
	"--enable":        true, "--disable": true,
	"--color":            true,
	"--ask-for-approval": true,
}

var codexBoolFlags = map[string]bool{
	"--json":              true,
	"--experimental-json": true,
	"--strict-config":     true,
	"--oss":               true,
	"--approve-for-me":    true,
	"--full-auto":         true,
	"--yolo":              true,
	"--dangerously-bypass-approvals-and-sandbox": true,
	"--dangerously-bypass-hook-trust":            true,
	"--worktree":                                 true,
	"--skip-git-repo-check":                      true,
	"--ignore-user-config":                       true,
	"--ignore-rules":                             true,
	"--search":                                   true,
	"--no-alt-screen":                            true,
	"--no-daemon":                                true,
	"--include-non-interactive":                  true,
}

func preservedCodexFlags(original []string, nonInteractive bool) []string {
	var out []string
	start := 0
	if len(original) > 0 && nonInteractive {
		start = 1 // drop the original `exec`/`e` subcommand
	}
	for i := start; i < len(original); i++ {
		arg := original[i]
		if arg == "--" {
			break
		}
		name, _, hasValue := strings.Cut(arg, "=")
		if codexBoolFlags[name] {
			// exec's --json is valid on exec resume. The interactive resume
			// command has no JSON mode, so omit it there.
			if (name == "--json" || name == "--experimental-json") && !nonInteractive {
				continue
			}
			out = append(out, arg)
			continue
		}
		if codexValueFlags[name] {
			if hasValue {
				out = append(out, arg)
				continue
			}
			if i+1 < len(original) && original[i+1] != "--" {
				out = append(out, arg, original[i+1])
				i++
			}
		}
	}
	return out
}

// promptForProfileSwitch is intentionally separate from cmdRun so the
// interaction can be tested without launching Codex. Only profiles with a
// parked auth file are shown: those are the profiles that can be switched to
// immediately without asking the user to log in first.
func promptForProfileSwitch(p *Paths, s *Store, current string, input io.Reader, output io.Writer) (bool, string, error) {
	names := make([]string, 0, len(s.Profiles))
	for _, name := range sortedNames(s) {
		if name != current && fileExists(p.profileFile(name)) {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		fmt.Fprintf(output, "\nCodex stopped with a possible usage/rate limit for profile %q, but no other parked profiles are available.\n", current)
		fmt.Fprintln(output, "Run `cx stats` later, or add another profile before retrying.")
		return false, "", nil
	}

	reader := bufio.NewReader(input)
	fmt.Fprintf(output, "\nCodex stopped with a possible usage/rate limit for profile %q.\n", current)
	fmt.Fprint(output, "Switch to another profile and resume this session? [y/N] ")
	answer, err := readPromptLine(reader)
	if err != nil {
		return false, "", err
	}
	if answer = strings.ToLower(answer); answer != "y" && answer != "yes" {
		fmt.Fprintln(output, "Keeping the current profile. The session was not resumed.")
		return false, "", nil
	}

	fmt.Fprintln(output, "\nAvailable profiles:")
	for i, name := range names {
		meta := s.Profiles[name]
		label := name
		if meta.Nickname != "" {
			label += " (" + meta.Nickname + ")"
		}
		fmt.Fprintf(output, "  [%d] %s\n", i+1, label)
	}
	fmt.Fprint(output, "Switch to profile number or name (blank to cancel): ")
	selection, err := readPromptLine(reader)
	if err != nil {
		return false, "", err
	}
	if selection == "" {
		fmt.Fprintln(output, "Cancelled. The session was not resumed.")
		return false, "", nil
	}
	if index, err := parseProfileIndex(selection, len(names)); err == nil {
		return true, names[index], nil
	}
	for _, name := range names {
		if selection == name {
			return true, name, nil
		}
	}
	return false, "", fmt.Errorf("invalid profile selection %q", selection)
}

func readPromptLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if err != nil && len(line) == 0 {
		if errors.Is(err, io.EOF) {
			return "", nil
		}
		return "", err
	}
	return line, nil
}

func parseProfileIndex(value string, count int) (int, error) {
	var index int
	if _, err := fmt.Sscanf(value, "%d", &index); err != nil || index < 1 || index > count {
		return 0, fmt.Errorf("profile index out of range")
	}
	return index - 1, nil
}
