package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestCmdCorpusRejectsNonPositiveOutlineBudget(t *testing.T) {
	err := cmdCorpus(t.Context(), []string{
		"--upstream", "pkg:npm/ws",
		"--out", t.TempDir(),
		"--outline-bytes", "0",
	})
	if err == nil || !strings.Contains(err.Error(), "--outline-bytes must be greater than zero") {
		t.Fatalf("error = %v", err)
	}
}

func TestCmdCorpusReturnsAllDependentFailures(t *testing.T) {
	first := "file:///missing-first"
	second := "file:///missing-second"
	err := cmdCorpus(t.Context(), []string{
		"--upstream", "pkg:npm/example",
		"--out", t.TempDir(),
		"--work", t.TempDir(),
		"--dependent", first,
		"--dependent", second,
	})
	if err == nil {
		t.Fatal("cmdCorpus discarded dependent failures")
	}
	for _, want := range []string{first, second} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("cmdCorpus error %q does not include %q", err, want)
		}
	}
}

func TestCmdCorpusRejectsEmbeddedDependentCredentialsWithoutEchoingThem(t *testing.T) {
	for _, spec := range []string{
		"https://secret@example.com/owner/repo",
		"https://user:secret@example.com/owner/repo@main",
		"https://user:sec%72et@example.com/owner/repo",
		"https://user:sec%ZZret@example.com/owner/repo",
	} {
		stderr, err := captureStderr(t, func() error {
			return cmdCorpus(t.Context(), []string{
				"--upstream", "pkg:npm/example",
				"--out", t.TempDir(),
				"--work", t.TempDir(),
				"--dependent", spec,
			})
		})
		if err == nil || (!strings.Contains(err.Error(), "credential helper") && !strings.Contains(err.Error(), "invalid repository URL")) {
			t.Fatalf("cmdCorpus(%q) error = %v", spec, err)
		}
		if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "sec%72et") || strings.Contains(err.Error(), "sec%ZZret") {
			t.Fatalf("cmdCorpus error exposed URL credentials: %q", err)
		}
		if strings.Contains(stderr, "secret") || strings.Contains(stderr, "sec%72et") || strings.Contains(stderr, "sec%ZZret") {
			t.Fatalf("cmdCorpus stderr exposed URL credentials: %q", stderr)
		}
	}
}

func captureStderr(t *testing.T, run func() error) (string, error) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stderr
	os.Stderr = writer
	runErr := run()
	os.Stderr = original
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	return string(body), runErr
}
