package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestIsQuotaFailure(t *testing.T) {
	for _, output := range []string{
		"You've hit your usage limit. Try again later.",
		"rate_limit_reached",
		"HTTP 429 Too Many Requests",
		"insufficient_quota",
	} {
		if !isQuotaFailure(runResult{output: output}) {
			t.Errorf("isQuotaFailure(%q) = false", output)
		}
	}
	for _, output := range []string{
		"Codex completed the task successfully.",
		"rate limits: primary 42% used",
		"permission denied",
	} {
		if isQuotaFailure(runResult{output: output}) {
			t.Errorf("isQuotaFailure(%q) = true", output)
		}
	}
}

func TestExtractThreadID(t *testing.T) {
	output := strings.Join([]string{
		"warning: starting",
		`{"type":"thread.started","thread_id":"thread-123"}`,
		`{"type":"turn.completed"}`,
	}, "\n")
	if got := extractThreadID(output); got != "thread-123" {
		t.Fatalf("thread id = %q, want thread-123", got)
	}
}

func TestResumeArgsPreserveExecSettings(t *testing.T) {
	original := []string{
		"exec", "--json", "--skip-git-repo-check", "-C", "/tmp/project",
		"--model", "gpt-test", "-s", "workspace-write", "finish the task",
	}
	want := []string{
		"exec", "--json", "--skip-git-repo-check", "-C", "/tmp/project",
		"--model", "gpt-test", "-s", "workspace-write", "resume", "thread-123",
		"Continue the task from where it stopped. Pick up from the last incomplete step and finish it.",
	}
	if got := resumeArgs(original, "thread-123"); strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("resume args = %#v, want %#v", got, want)
	}
}

func TestResumeArgsInteractiveDropsJSON(t *testing.T) {
	original := []string{"--json", "-C", "/tmp/project", "--model", "gpt-test", "do work"}
	want := []string{"resume", "-C", "/tmp/project", "--model", "gpt-test", "--last"}
	if got := resumeArgs(original, ""); strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("resume args = %#v, want %#v", got, want)
	}
}

func TestPromptForProfileSwitch(t *testing.T) {
	p := testPaths(t)
	s := testStore("work")
	s.Profiles["personal"] = &ProfileMeta{Name: "personal", Nickname: "Personal"}
	if err := os.WriteFile(p.profileFile("personal"), []byte("personal auth"), 0o600); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	resume, selected, err := promptForProfileSwitch(p, s, "work", strings.NewReader("yes\n1\n"), &output)
	if err != nil {
		t.Fatal(err)
	}
	if !resume || selected != "personal" {
		t.Fatalf("prompt result = (%t, %q), want (true, personal)", resume, selected)
	}
	if !strings.Contains(output.String(), "personal (Personal)") {
		t.Fatalf("prompt did not list the available profile: %s", output.String())
	}
}

func TestPromptForProfileSwitchSkipsMissingParkedFiles(t *testing.T) {
	p := testPaths(t)
	s := testStore("work")
	s.Profiles["missing"] = &ProfileMeta{Name: "missing"}

	var output bytes.Buffer
	resume, selected, err := promptForProfileSwitch(p, s, "work", strings.NewReader("y\n"), &output)
	if err != nil {
		t.Fatal(err)
	}
	if resume || selected != "" {
		t.Fatalf("prompt result = (%t, %q), want no switch", resume, selected)
	}
	if !strings.Contains(output.String(), "no other parked profiles") {
		t.Fatalf("prompt did not explain why switching was unavailable: %s", output.String())
	}
}

func TestRunRejectsUnsupportedCodexSubcommand(t *testing.T) {
	p := testPaths(t)
	s := testStore("work")
	s.Active = "work"
	if err := os.WriteFile(p.AuthPath, []byte("auth"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmdRun(p, s, []string{"login"}); err == nil || !strings.Contains(err.Error(), "cannot resume") {
		t.Fatalf("cmdRun error = %v, want unsupported-command error", err)
	}
}
